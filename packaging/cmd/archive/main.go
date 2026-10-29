// Command archive creates release archives without requiring a host zip tool.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type options struct {
	format string
	source string
	output string
}

func main() {
	opts := options{}
	flag.StringVar(&opts.format, "format", "", "archive format: zip or tar.gz")
	flag.StringVar(&opts.source, "source", "", "directory to archive")
	flag.StringVar(&opts.output, "output", "", "archive output path")
	flag.Parse()
	if err := archive(opts); err != nil {
		fmt.Fprintln(os.Stderr, "archive:", err)
		os.Exit(1)
	}
}

func archive(opts options) error {
	if opts.format != "zip" && opts.format != "tar.gz" {
		return fmt.Errorf("unsupported format %q", opts.format)
	}
	if strings.TrimSpace(opts.source) == "" || strings.TrimSpace(opts.output) == "" {
		return errors.New("source and output are required")
	}
	info, err := os.Stat(opts.source)
	if err != nil {
		return fmt.Errorf("inspect source: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", opts.source)
	}

	return writeAtomically(opts.output, func(file *os.File) error {
		switch opts.format {
		case "zip":
			return writeZip(file, opts.source)
		case "tar.gz":
			return writeTarGz(file, opts.source)
		default:
			return fmt.Errorf("unsupported format %q", opts.format)
		}
	})
}

func writeAtomically(output string, write func(*os.File) error) error {
	outputDir := filepath.Dir(output)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	temporary, err := os.CreateTemp(outputDir, "."+filepath.Base(output)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary archive: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()

	if err := write(temporary); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary archive: %w", err)
	}
	if err := os.Chmod(temporaryName, 0o644); err != nil {
		return fmt.Errorf("set archive permissions: %w", err)
	}
	if err := os.Rename(temporaryName, output); err != nil {
		return fmt.Errorf("replace archive: %w", err)
	}
	removeTemporary = false
	return nil
}

func writeZip(output io.Writer, source string) error {
	writer := zip.NewWriter(output)
	err := walkArchiveTree(source, func(path string, entry fs.DirEntry, name string) error {
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not supported: %s", path)
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return fmt.Errorf("create zip header for %s: %w", path, err)
		}
		header.Name = name
		header.SetMode(info.Mode())
		if entry.IsDir() {
			header.Name += "/"
			header.Method = zip.Store
		}
		fileWriter, err := writer.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("create zip entry for %s: %w", path, err)
		}
		if entry.IsDir() {
			return nil
		}
		input, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		defer input.Close()
		if _, err := io.Copy(fileWriter, input); err != nil {
			return fmt.Errorf("copy %s: %w", path, err)
		}
		return nil
	})
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	return err
}

func writeTarGz(output io.Writer, source string) error {
	compressed := gzip.NewWriter(output)
	writer := tar.NewWriter(compressed)
	err := walkArchiveTree(source, func(path string, entry fs.DirEntry, name string) error {
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not supported: %s", path)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("create tar header for %s: %w", path, err)
		}
		header.Name = name
		if err := writer.WriteHeader(header); err != nil {
			return fmt.Errorf("write tar header for %s: %w", path, err)
		}
		if entry.IsDir() {
			return nil
		}
		input, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		defer input.Close()
		if _, err := io.Copy(writer, input); err != nil {
			return fmt.Errorf("copy %s: %w", path, err)
		}
		return nil
	})
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if closeErr := compressed.Close(); err == nil {
		err = closeErr
	}
	return err
}

func walkArchiveTree(source string, visit func(path string, entry fs.DirEntry, name string) error) error {
	base := filepath.Dir(filepath.Clean(source))
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		return visit(path, entry, filepath.ToSlash(relative))
	})
}
