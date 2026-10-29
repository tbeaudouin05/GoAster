// Package attachment contains the local contract used to pass inbound files
// from a channel adapter to the agent.
package attachment

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/image/vp8"
	"golang.org/x/image/vp8l"
	"golang.org/x/image/webp"
)

const (
	// MaxBytes is both the Telegram download limit and the maximum size the
	// loopback agent will accept in an attachment payload.
	MaxBytes int64 = 20 << 20

	// MaxAttachments keeps the first implementation deliberately scoped to a
	// single photo or document per message. Telegram photo sizes are not a
	// media album and are reduced to one selected photo by the adapter.
	MaxAttachments = 1

	maxFilenameBytes = 180

	// AgentTemporaryDirectoryPrefix names agent-owned, per-request directories.
	AgentTemporaryDirectoryPrefix = "goaster-agent-attachment-"

	// Limit both a single decoded image and the aggregate frames of a GIF.
	MaxImageDimension = 8192
	MaxImagePixels    = 20_000_000
	maxGIFFrames      = 1000
)

var (
	// ErrUnsupportedImage is returned when bytes claim to be an image but are
	// not one of the supported, decodable image formats.
	ErrUnsupportedImage = errors.New("unsupported image format")
	errNotImage         = errors.New("not an image")
)

// Metadata identifies a file that is available locally for the current chat
// turn. The path is temporary and must not be persisted as conversation data.
type Metadata struct {
	Type     string `json:"type"`
	Filename string `json:"filename"`
	Path     string `json:"path"`
	MIMEType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size"`
}

// Incoming is the loopback wire contract. JSON encodes Data as base64; no
// client-supplied file path is accepted or used by the agent.
type Incoming struct {
	Type     string `json:"type"`
	Filename string `json:"filename"`
	MIMEType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size"`
	Data     []byte `json:"data"`
}

// FileInspection describes the bytes of one downloaded attachment. MIMEType
// is derived from the bytes, never from Telegram metadata or a filename.
type FileInspection struct {
	Size     int64
	MIMEType string
}

// SanitizeFilename returns a portable basename that cannot add path
// components or shell metacharacters when it is used as a local filename.
func SanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	if name == "." || name == ".." || name == "" {
		return "attachment"
	}

	var safe strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), strings.ContainsRune("._-+()[]@", r):
			safe.WriteRune(r)
		default:
			safe.WriteByte('_')
		}
	}
	name = strings.TrimRight(safe.String(), ". ")
	name = truncateUTF8(name, maxFilenameBytes)
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	return name
}

// ValidateMetadata checks fields that are safe to carry through the model
// request without inspecting the file system.
func (a Metadata) ValidateMetadata() error {
	if err := validateFields(a.Type, a.Filename, a.MIMEType, a.Size); err != nil {
		return err
	}
	if a.Path == "" || !filepath.IsAbs(a.Path) || filepath.Clean(a.Path) != a.Path || strings.IndexFunc(a.Path, unicode.IsControl) >= 0 {
		return fmt.Errorf("unsafe attachment path")
	}
	return nil
}

func validateFields(kind, filename, mimeType string, size int64) error {
	if kind != "image" && kind != "document" {
		return fmt.Errorf("unsupported attachment type")
	}
	if filename == "" || filename != SanitizeFilename(filename) || filepath.Base(filename) != filename {
		return fmt.Errorf("unsafe attachment filename")
	}
	if len(filename) > maxFilenameBytes || !utf8.ValidString(filename) {
		return fmt.Errorf("attachment filename is too long or invalid")
	}
	if size < 0 || size > MaxBytes {
		return fmt.Errorf("attachment size is out of range")
	}
	if len(mimeType) > 128 || !utf8.ValidString(mimeType) || strings.IndexFunc(mimeType, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid attachment MIME type")
	}
	if mimeType != "" {
		if _, _, err := mime.ParseMediaType(mimeType); err != nil {
			return fmt.Errorf("invalid attachment MIME type")
		}
	}
	return nil
}

// Validate checks the wire metadata and bytes and derives image type from
// content. The returned metadata has no path until the agent creates a file.
func (a Incoming) Validate() (Metadata, error) {
	if err := validateFields(a.Type, a.Filename, a.MIMEType, a.Size); err != nil {
		return Metadata{}, err
	}
	if a.Data == nil || int64(len(a.Data)) != a.Size {
		return Metadata{}, fmt.Errorf("attachment size does not match data")
	}
	inspection, err := InspectBytes(a.Data)
	if err != nil {
		return Metadata{}, err
	}
	metadata := Metadata{Type: a.Type, Filename: a.Filename, MIMEType: a.MIMEType, Size: a.Size}
	if inspection.MIMEType != "" {
		metadata.Type = "image"
		metadata.MIMEType = inspection.MIMEType
	} else if a.Type == "image" || claimedImage(a.Filename, a.MIMEType) {
		return Metadata{}, ErrUnsupportedImage
	}
	return metadata, nil
}

func claimedImage(filename, mimeType string) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(mimeType)), "image/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".avif", ".bmp", ".gif", ".heic", ".heif", ".ico", ".j2k", ".j2c", ".jpeg", ".jpg", ".jxl", ".png", ".svg", ".tif", ".tiff", ".webp":
		return true
	default:
		return false
	}
}

// Validate checks that metadata refers to a private, regular file in a
// per-message child of the process temporary directory. This prevents the
// local tools only receive private files for the current turn.
func (a Metadata) Validate() error {
	if err := a.ValidateMetadata(); err != nil {
		return err
	}
	root, err := filepath.Abs(filepath.Clean(os.TempDir()))
	if err != nil {
		return fmt.Errorf("inspect temporary directory")
	}
	pathName, err := filepath.Abs(a.Path)
	if err != nil || pathName != a.Path {
		return fmt.Errorf("unsafe attachment path")
	}
	parent := filepath.Dir(pathName)
	if parent == root || !within(root, parent) || filepath.Base(pathName) != a.Filename {
		return fmt.Errorf("attachment path is not a per-message temporary file")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("inspect temporary directory")
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil || !within(resolvedRoot, resolvedParent) {
		return fmt.Errorf("attachment path is not a per-message temporary file")
	}

	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() || parentInfo.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("attachment directory is not private")
	}
	info, err := os.Lstat(pathName)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("attachment file is not a regular file")
	}
	if info.Mode().Perm()&0077 != 0 || info.Mode().Perm()&0400 == 0 {
		return fmt.Errorf("attachment file is not private")
	}
	if info.Size() != a.Size {
		return fmt.Errorf("attachment size changed")
	}
	return nil
}

// InspectFile reads at most maxBytes and identifies supported image bytes.
// Non-image files are valid documents and return an empty MIMEType. A file
// that is recognizably an unsupported or malformed image returns
// ErrUnsupportedImage.
func InspectFile(pathName string, maxBytes int64) (FileInspection, error) {
	data, _, err := readBoundedFile(pathName, maxBytes)
	if err != nil {
		return FileInspection{}, err
	}
	return InspectBytes(data)
}

// InspectBytes identifies supported images by their content. Non-images can
// be documents; recognizable malformed or unsupported images are rejected.
func InspectBytes(data []byte) (FileInspection, error) {
	if int64(len(data)) > MaxBytes {
		return FileInspection{}, fmt.Errorf("attachment exceeds size limit")
	}
	mimeType, err := detectImageMIME(data)
	if errors.Is(err, errNotImage) {
		return FileInspection{Size: int64(len(data))}, nil
	}
	if err != nil {
		return FileInspection{}, err
	}
	return FileInspection{Size: int64(len(data)), MIMEType: mimeType}, nil
}

// ReadImageDataURL validates and reads one current-turn image. The returned
// base64 string is intended only for the in-flight Responses request; callers
// must not put it in conversation history or persistent state.
func ReadImageDataURL(a Metadata) (string, error) {
	if a.Type != "image" {
		return "", fmt.Errorf("attachment is not an image")
	}
	if err := a.Validate(); err != nil {
		return "", fmt.Errorf("invalid image attachment")
	}
	data, size, err := readBoundedFile(a.Path, MaxBytes)
	if err != nil {
		return "", fmt.Errorf("read image attachment: %w", err)
	}
	if size != a.Size {
		return "", fmt.Errorf("image attachment size changed")
	}
	mimeType, err := detectImageMIME(data)
	if err != nil {
		if errors.Is(err, errNotImage) {
			return "", ErrUnsupportedImage
		}
		return "", err
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// CleanupTemporaryDir removes one private temporary attachment directory.
func CleanupTemporaryDir(directory string) error {
	if strings.TrimSpace(directory) == "" {
		return errors.New("attachment directory is empty")
	}
	root, err := filepath.Abs(filepath.Clean(os.TempDir()))
	if err != nil {
		return errors.New("inspect temporary directory")
	}
	clean, err := filepath.Abs(directory)
	if err != nil || clean != directory || clean == root || !within(root, clean) {
		return errors.New("unsafe attachment directory")
	}
	info, err := os.Lstat(clean)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	resolvedRoot, rootErr := filepath.EvalSymlinks(root)
	resolvedDirectory, directoryErr := filepath.EvalSymlinks(clean)
	if err != nil || rootErr != nil || directoryErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0077 != 0 || !within(resolvedRoot, resolvedDirectory) {
		return errors.New("attachment directory is not private")
	}
	return os.RemoveAll(clean)
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// AddReferences adds current-turn attachment metadata to the model-facing
// message. Callers should keep the original message for conversation history
// because these paths are intentionally temporary.
func AddReferences(message string, attachments []Metadata) string {
	if len(attachments) == 0 {
		return message
	}
	var result strings.Builder
	if message != "" {
		result.WriteString(message)
		result.WriteString("\n\n")
	}
	result.WriteString("Attachments available for this message. Images are included for visual analysis; documents are temporary local files that local tools can read:\n")
	for _, a := range attachments {
		fmt.Fprintf(&result, "- type: %s; filename: %s; mime_type: %s; size_bytes: %d; local_path: %s\n", a.Type, a.Filename, a.MIMEType, a.Size, a.Path)
	}
	return result.String()
}

// HistoryPlaceholder keeps an attachment-only user turn meaningful without
// retaining a temporary path or any encoded file content.
func HistoryPlaceholder(message string, attachments []Metadata) string {
	if strings.TrimSpace(message) != "" || len(attachments) == 0 {
		return message
	}
	var result strings.Builder
	result.WriteString("User sent attachment")
	if len(attachments) != 1 {
		result.WriteString("s")
	}
	result.WriteString(": ")
	for index, a := range attachments {
		if index > 0 {
			result.WriteString(", ")
		}
		fmt.Fprintf(&result, "%s %s", a.Type, a.Filename)
	}
	return result.String()
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func readBoundedFile(pathName string, maxBytes int64) ([]byte, int64, error) {
	if maxBytes <= 0 || maxBytes > MaxBytes {
		maxBytes = MaxBytes
	}
	info, err := os.Stat(pathName)
	if err != nil || !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("attachment file is not readable")
	}
	if info.Size() < 0 || info.Size() > maxBytes {
		return nil, 0, fmt.Errorf("attachment exceeds size limit")
	}
	file, err := os.Open(pathName)
	if err != nil {
		return nil, 0, fmt.Errorf("attachment file is not readable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("read attachment file")
	}
	if int64(len(data)) > maxBytes || int64(len(data)) != info.Size() {
		return nil, 0, fmt.Errorf("attachment size changed")
	}
	return data, int64(len(data)), nil
}

func detectImageMIME(data []byte) (string, error) {
	isWebP := len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP"))
	var config image.Config
	var format string
	var err error
	if isWebP {
		config, err = webp.DecodeConfig(bytes.NewReader(data))
		format = "webp"
	} else {
		config, format, err = image.DecodeConfig(bytes.NewReader(data))
	}
	if err != nil {
		if looksLikeImage(data) {
			return "", ErrUnsupportedImage
		}
		return "", errNotImage
	}
	if !validDimensions(config.Width, config.Height) {
		return "", ErrUnsupportedImage
	}
	switch format {
	case "gif":
		// DecodeAll retains every frame. Inspect frame sizes and their total
		// first so animated GIFs cannot allocate an unbounded amount.
		if !validGIFFrames(data, config.Width, config.Height) {
			return "", ErrUnsupportedImage
		}
		if _, err = gif.DecodeAll(bytes.NewReader(data)); err != nil {
			return "", ErrUnsupportedImage
		}
	case "webp":
		// An extended WebP header can claim a smaller canvas than the VP8
		// frame. Check both before the decoder allocates either image.
		if !validWebPFrames(data, config.Width, config.Height) {
			return "", ErrUnsupportedImage
		}
		if _, err = webp.Decode(bytes.NewReader(data)); err != nil {
			return "", ErrUnsupportedImage
		}
	case "jpeg", "png":
		if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
			return "", ErrUnsupportedImage
		}
	default:
		return "", ErrUnsupportedImage
	}
	return "image/" + format, nil
}

func validDimensions(width, height int) bool {
	return width > 0 && height > 0 && width <= MaxImageDimension && height <= MaxImageDimension && int64(width)*int64(height) <= MaxImagePixels
}

func validWebPFrames(data []byte, width, height int) bool {
	if len(data) < 12 {
		return false
	}
	size := uint64(binary.LittleEndian.Uint32(data[4:8])) + 8
	if size > uint64(len(data)) || size < 12 {
		return false
	}
	end := int(size)
	offset := 12
	frameSeen := false
	for offset <= end-8 {
		chunkType := string(data[offset : offset+4])
		chunkSize := uint64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		offset += 8
		if chunkSize+(chunkSize&1) > uint64(end-offset) {
			return false
		}
		chunk := data[offset : offset+int(chunkSize)]
		switch chunkType {
		case "VP8 ":
			decoder := vp8.NewDecoder()
			decoder.Init(bytes.NewReader(chunk), len(chunk))
			frame, err := decoder.DecodeFrameHeader()
			if frameSeen || err != nil || frame.Width != width || frame.Height != height || !validDimensions(frame.Width, frame.Height) {
				return false
			}
			frameSeen = true
		case "VP8L":
			frame, err := vp8l.DecodeConfig(bytes.NewReader(chunk))
			if frameSeen || err != nil || frame.Width != width || frame.Height != height || !validDimensions(frame.Width, frame.Height) {
				return false
			}
			frameSeen = true
		}
		offset += int(chunkSize)
		if chunkSize&1 != 0 {
			offset++
		}
	}
	return frameSeen && offset == end
}

// validGIFFrames scans block boundaries before gif.DecodeAll allocates frames.
func validGIFFrames(data []byte, width, height int) bool {
	if len(data) < 13 {
		return false
	}
	offset := 13
	if data[10]&0x80 != 0 {
		offset += 3 * (1 << (uint(data[10]&7) + 1))
	}
	var pixels int64
	frames := 0
	for offset < len(data) {
		block := data[offset]
		offset++
		switch block {
		case 0x3b: // trailer
			return frames > 0
		case 0x21: // extension: label followed by sub-blocks
			if offset >= len(data) {
				return false
			}
			offset++
		case 0x2c: // image descriptor, optional color table, LZW code size
			if len(data)-offset < 9 {
				return false
			}
			left := int(binary.LittleEndian.Uint16(data[offset:]))
			top := int(binary.LittleEndian.Uint16(data[offset+2:]))
			frameWidth := int(binary.LittleEndian.Uint16(data[offset+4:]))
			frameHeight := int(binary.LittleEndian.Uint16(data[offset+6:]))
			packed := data[offset+8]
			offset += 9
			if !validDimensions(frameWidth, frameHeight) || left+frameWidth > width || top+frameHeight > height {
				return false
			}
			frames++
			pixels += int64(frameWidth) * int64(frameHeight)
			if frames > maxGIFFrames || pixels > MaxImagePixels {
				return false
			}
			if packed&0x80 != 0 {
				offset += 3 * (1 << (uint(packed&7) + 1))
			}
			if offset >= len(data) {
				return false
			}
			offset++
		default:
			return false
		}
		// Every extension and image data stream ends with a zero-size block.
		for {
			if offset >= len(data) {
				return false
			}
			size := int(data[offset])
			offset++
			if size == 0 {
				break
			}
			if size > len(data)-offset {
				return false
			}
			offset += size
		}
	}
	return false
}

func looksLikeImage(data []byte) bool {
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return true
	}
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return true
	}
	if len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))) {
		return true
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return true
	}
	if len(data) >= 2 && bytes.Equal(data[:2], []byte("BM")) {
		return true
	}
	if len(data) >= 4 && (bytes.Equal(data[:4], []byte{'I', 'I', 42, 0}) || bytes.Equal(data[:4], []byte{'M', 'M', 0, 42}) || bytes.Equal(data[:4], []byte{0, 0, 1, 0})) {
		return true
	}
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) {
		switch string(data[8:12]) {
		case "avif", "avis", "heic", "heix", "hevc", "hevx", "mif1", "msf1":
			return true
		}
	}
	textSample := strings.ToLower(strings.TrimSpace(string(data[:min(len(data), 512)])))
	if strings.HasPrefix(textSample, "<svg") || strings.HasPrefix(textSample, "<?xml") && strings.Contains(textSample, "<svg") {
		return true
	}
	return strings.HasPrefix(http.DetectContentType(data), "image/")
}
