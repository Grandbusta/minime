// Package archive is data a work carries: files with their paths, like the
// code of a small program, made into a zip.
package archive

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/Grandbusta/minime/internal/memory"
)

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type Files struct {
	Files []File `json:"files"`
}

const (
	maxFiles = 100
	maxBytes = 2 << 20 // all the files together
)

var ErrEmpty = errors.New("archive: no files with a path that can be kept")

// Parse reads files as JSON and keeps those with a safe path: relative,
// inside the zip (no ".."), once each; up to maxFiles and maxBytes in all.
func Parse(raw []byte) (Files, error) {
	var in Files
	if err := json.Unmarshal(raw, &in); err != nil {
		return Files{}, fmt.Errorf("archive: %w", err)
	}
	var out Files
	seen := map[string]bool{}
	size := 0
	for _, f := range in.Files {
		p := path.Clean(strings.ReplaceAll(strings.TrimSpace(f.Path), `\`, "/"))
		if p == "." || path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") || seen[p] ||
			len(out.Files) == maxFiles || size+len(f.Content) > maxBytes {
			continue
		}
		seen[p], size = true, size+len(f.Content)
		out.Files = append(out.Files, File{Path: p, Content: f.Content})
	}
	if len(out.Files) == 0 {
		return Files{}, ErrEmpty
	}
	return out, nil
}

type Format struct{}

func (Format) Name() string      { return "zip" }
func (Format) Ext() string       { return "zip" }
func (Format) MediaType() string { return "application/zip" }
func (Format) Describe() string  { return "a zip of the files, ready to open or run" }
func (Format) WriteHelp() string { return "" }
func (Format) Needs() string     { return "files" }

func (Format) DataHelp() string {
	return "every file of it in full, each with its path (like main.go or src/app.js), so it works as it is once unzipped; " +
		"a short README.md saying what it is and how to run it."
}

var schema = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"files"},
	"properties": map[string]any{
		"files": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"path", "content"},
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
		}},
	}}

func (Format) DataSchema() map[string]any { return schema }

func (Format) CleanData(raw json.RawMessage) (json.RawMessage, error) {
	fs, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fs)
}

func (Format) Render(w memory.Work, _ string) ([]byte, error) {
	fs, err := Parse(w.Data)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range fs.Files {
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: f.Path, Method: zip.Deflate, Modified: w.CreatedAt})
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write([]byte(f.Content)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
