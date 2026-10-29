package attachment

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/webp"
)

func TestSanitizeFilenameRemovesPathAndUnsafeCharacters(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "../../secret.txt", want: "secret.txt"},
		{input: `..\secret name?.txt`, want: "secret_name_.txt"},
		{input: "...", want: "attachment"},
		{input: " report.txt ", want: "report.txt"},
	} {
		if got := SanitizeFilename(test.input); got != test.want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", test.input, got, test.want)
		}
		if strings.ContainsAny(SanitizeFilename(test.input), "/\\\x00\r\n") {
			t.Errorf("sanitized filename %q still contains path or control characters", SanitizeFilename(test.input))
		}
	}
}

func TestMetadataValidateRequiresPrivatePerMessageFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	pathName := filepath.Join(directory, "report.txt")
	data := []byte("test")
	if err := os.WriteFile(pathName, data, 0600); err != nil {
		t.Fatal(err)
	}
	metadata := Metadata{Type: "document", Filename: "report.txt", Path: pathName, MIMEType: "text/plain", Size: int64(len(data))}
	if err := metadata.Validate(); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}

	for name, invalid := range map[string]Metadata{
		"traversal filename":  {Type: "document", Filename: "../report.txt", Path: pathName, Size: int64(len(data))},
		"normalized path":     {Type: "document", Filename: "report.txt", Path: directory + string(os.PathSeparator) + "." + string(os.PathSeparator) + "report.txt", Size: int64(len(data))},
		"size mismatch":       {Type: "document", Filename: "report.txt", Path: pathName, Size: 99},
		"unsupported type":    {Type: "video", Filename: "report.txt", Path: pathName, Size: int64(len(data))},
		"world readable file": {Type: "document", Filename: "report.txt", Path: pathName, Size: int64(len(data))},
	} {
		if name == "world readable file" {
			if err := os.Chmod(pathName, 0644); err != nil {
				t.Fatal(err)
			}
		}
		if err := invalid.Validate(); err == nil {
			t.Errorf("%s metadata was accepted", name)
		}
		if name == "world readable file" {
			if err := os.Chmod(pathName, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestMetadataValidateRejectsOversizedAndSymlinkFiles(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	pathName := filepath.Join(directory, "large.bin")
	data := []byte("large")
	if err := os.WriteFile(pathName, data, 0600); err != nil {
		t.Fatal(err)
	}
	metadata := Metadata{Type: "document", Filename: "large.bin", Path: pathName, Size: MaxBytes + 1}
	if err := metadata.Validate(); err == nil {
		t.Fatal("oversized metadata was accepted")
	}

	symlink := filepath.Join(directory, "link.bin")
	if err := os.Symlink(pathName, symlink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	metadata = Metadata{Type: "document", Filename: "link.bin", Path: symlink, Size: int64(len(data))}
	if err := metadata.Validate(); err == nil {
		t.Fatal("symlink metadata was accepted")
	}
}

func TestCleanupTemporaryDirIsScoped(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "file.txt")
	if err := os.WriteFile(file, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CleanupTemporaryDir(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("temporary directory still exists, stat error = %v", err)
	}
	if err := CleanupTemporaryDir(os.TempDir()); err == nil {
		t.Fatal("accepted the process temporary directory as a cleanup target")
	}
}

func TestAddReferencesKeepsMessageAndIncludesUsefulMetadata(t *testing.T) {
	metadata := Metadata{Type: "image", Filename: "photo.jpg", Path: "/tmp/goaster-attachment/photo.jpg", MIMEType: "image/jpeg", Size: 123}
	got := AddReferences("look at this", []Metadata{metadata})
	for _, want := range []string{"look at this", "photo.jpg", "/tmp/goaster-attachment/photo.jpg", "image/jpeg", "123"} {
		if !strings.Contains(got, want) {
			t.Errorf("attachment reference %q missing from %q", want, got)
		}
	}
}

func TestReadImageDataURLUsesValidatedBytesRatherThanMetadata(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	imageData := image.NewRGBA(image.Rect(0, 0, 1, 1))
	imageData.Set(0, 0, color.RGBA{R: 0x22, G: 0x66, B: 0xaa, A: 0xff})
	if err := png.Encode(&encoded, imageData); err != nil {
		t.Fatal(err)
	}
	pathName := filepath.Join(directory, "claimed.jpg")
	if err := os.WriteFile(pathName, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	metadata := Metadata{Type: "image", Filename: "claimed.jpg", Path: pathName, MIMEType: "image/jpeg", Size: int64(encoded.Len())}
	dataURL, err := ReadImageDataURL(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Fatalf("data URL = %q, want PNG MIME derived from bytes", dataURL[:min(len(dataURL), 64)])
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, encoded.Bytes()) {
		t.Fatal("data URL payload changed the image bytes")
	}
}

func TestImageValidationAcceptsSupportedFormatsAndRejectsUnsupportedBytes(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	images := []struct {
		name     string
		mimeType string
		encode   func(io.Writer, image.Image) error
	}{
		{name: "photo.jpg", mimeType: "image/jpeg", encode: func(writer io.Writer, value image.Image) error { return jpeg.Encode(writer, value, nil) }},
		{name: "photo.png", mimeType: "image/png", encode: png.Encode},
		{name: "photo.gif", mimeType: "image/gif", encode: func(writer io.Writer, value image.Image) error { return gif.Encode(writer, value, nil) }},
	}
	imageData := image.NewRGBA(image.Rect(0, 0, 1, 1))
	imageData.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	for _, test := range images {
		t.Run(test.name, func(t *testing.T) {
			var encoded bytes.Buffer
			if err := test.encode(&encoded, imageData); err != nil {
				t.Fatal(err)
			}
			pathName := filepath.Join(directory, test.name)
			if err := os.WriteFile(pathName, encoded.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			inspection, err := InspectFile(pathName, MaxBytes)
			if err != nil || inspection.MIMEType != test.mimeType {
				t.Fatalf("inspection = %+v, error = %v", inspection, err)
			}
			assertTruncatedImageRejected(t, encoded.Bytes(), false)
		})
	}
	webpBytes, err := base64.StdEncoding.DecodeString("UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA==")
	if err != nil {
		t.Fatal(err)
	}
	webpPath := filepath.Join(directory, "photo.webp")
	if err := os.WriteFile(webpPath, webpBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if inspection, err := InspectFile(webpPath, MaxBytes); err != nil || inspection.MIMEType != "image/webp" {
		t.Fatalf("WebP inspection = %+v, error = %v", inspection, err)
	}
	assertTruncatedImageRejected(t, webpBytes, true)
	// VP8X advertises a 1x1 canvas, while the enclosed VP8L frame retains
	// its real dimensions. The header alone must not authorize allocation.
	forged := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"), webpBytes[12:]...)
	binary.LittleEndian.PutUint32(forged[4:8], uint32(len(forged)-8))
	if config, err := webp.DecodeConfig(bytes.NewReader(forged)); err != nil || config.Width != 1 || config.Height != 1 {
		t.Fatalf("forged WebP config = %+v, error = %v", config, err)
	}
	if _, err := InspectBytes(forged); !errors.Is(err, ErrUnsupportedImage) {
		t.Fatalf("WebP with mismatched frame dimensions accepted: %v", err)
	}
	brokenTrailer := append(bytes.Clone(webpBytes), []byte{'E', 'X', 'I', 'F', 10, 0, 0, 0, 1}...)
	binary.LittleEndian.PutUint32(brokenTrailer[4:8], uint32(len(brokenTrailer)-8))
	if _, err := InspectBytes(brokenTrailer); !errors.Is(err, ErrUnsupportedImage) {
		t.Fatalf("WebP with a truncated trailing chunk accepted: %v", err)
	}

	unsupportedPath := filepath.Join(directory, "photo.jpg")
	if err := os.WriteFile(unsupportedPath, []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	metadata := Metadata{Type: "image", Filename: "photo.jpg", Path: unsupportedPath, Size: int64(len("not an image"))}
	if _, err := ReadImageDataURL(metadata); !errors.Is(err, ErrUnsupportedImage) {
		t.Fatalf("unsupported image error = %v, want ErrUnsupportedImage", err)
	}

	tooLargePath := filepath.Join(directory, "too-large.txt")
	if err := os.WriteFile(tooLargePath, []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectFile(tooLargePath, 4); err == nil {
		t.Fatal("bounded inspection accepted a file larger than its read limit")
	}
}

func assertTruncatedImageRejected(t *testing.T, data []byte, isWebP bool) {
	t.Helper()
	for end := 1; end < len(data); end++ {
		var configErr, decodeErr error
		if isWebP {
			_, configErr = webp.DecodeConfig(bytes.NewReader(data[:end]))
			_, decodeErr = webp.Decode(bytes.NewReader(data[:end]))
		} else {
			_, _, configErr = image.DecodeConfig(bytes.NewReader(data[:end]))
			_, _, decodeErr = image.Decode(bytes.NewReader(data[:end]))
		}
		if configErr == nil && decodeErr != nil {
			if _, err := InspectBytes(data[:end]); !errors.Is(err, ErrUnsupportedImage) {
				t.Fatalf("truncated image with valid header accepted: %v", err)
			}
			return
		}
	}
	t.Fatal("could not construct a truncated image with a valid header")
}

func TestImageDimensionAndAnimationLimits(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	for _, dimensions := range [][2]uint32{{MaxImageDimension + 1, 1}, {5000, 5000}} {
		data := bytes.Clone(encoded.Bytes())
		binary.BigEndian.PutUint32(data[16:20], dimensions[0])
		binary.BigEndian.PutUint32(data[20:24], dimensions[1])
		binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
		if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil {
			t.Fatalf("image header invalid before dimension check: %v", err)
		}
		if _, err := InspectBytes(data); !errors.Is(err, ErrUnsupportedImage) {
			t.Fatalf("oversized image %v accepted: %v", dimensions, err)
		}
	}

	frame := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black, color.White})
	animated := &gif.GIF{Image: make([]*image.Paletted, maxGIFFrames+1), Delay: make([]int, maxGIFFrames+1)}
	for i := range animated.Image {
		animated.Image[i] = frame
	}
	encoded.Reset()
	if err := gif.EncodeAll(&encoded, animated); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectBytes(encoded.Bytes()); !errors.Is(err, ErrUnsupportedImage) {
		t.Fatalf("GIF with too many frames accepted: %v", err)
	}
	animated.Image, animated.Delay = animated.Image[:2], animated.Delay[:2]
	encoded.Reset()
	if err := gif.EncodeAll(&encoded, animated); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectBytes(encoded.Bytes()); err != nil {
		t.Fatalf("valid animated GIF rejected: %v", err)
	}
	truncated := encoded.Bytes()[:encoded.Len()-5]
	if _, err := gif.Decode(bytes.NewReader(truncated)); err != nil {
		t.Fatalf("first GIF frame should remain decodable: %v", err)
	}
	if _, err := InspectBytes(truncated); !errors.Is(err, ErrUnsupportedImage) {
		t.Fatalf("GIF with malformed later frame accepted: %v", err)
	}

	// Each frame fits the individual limit, but retaining both exceeds the
	// aggregate animation budget. Minimal blocks suffice for the preflight.
	gifHeader := []byte{'G', 'I', 'F', '8', '9', 'a', 0x88, 0x13, 0xc4, 0x09, 0, 0, 0}
	frameBlock := []byte{0x2c, 0, 0, 0, 0, 0x88, 0x13, 0xc4, 0x09, 0, 2, 0}
	oneFrame := append(append(bytes.Clone(gifHeader), frameBlock...), 0x3b)
	twoFrames := append(append(append(bytes.Clone(gifHeader), frameBlock...), frameBlock...), 0x3b)
	if !validGIFFrames(oneFrame, 5000, 2500) || validGIFFrames(twoFrames, 5000, 2500) {
		t.Fatal("GIF aggregate pixel limit was not enforced")
	}
}

func TestHistoryPlaceholderDoesNotRetainTemporaryData(t *testing.T) {
	metadata := Metadata{Type: "image", Filename: "photo.png", Path: "/tmp/private/photo.png", MIMEType: "image/png", Size: 12}
	got := HistoryPlaceholder("", []Metadata{metadata})
	if got == "" || !strings.Contains(got, "photo.png") || strings.Contains(got, metadata.Path) || strings.Contains(got, "base64") {
		t.Fatalf("history placeholder = %q", got)
	}
}
