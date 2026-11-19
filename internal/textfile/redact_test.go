package textfile_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
)

func TestFile_Redacted(t *testing.T) {
	const content = "dev-only-fake-file-content"
	f := textfile.File{Encoding: textfile.UTF8, Text: []byte("key=" + content + "\n"), Hash: "abc123"}
	holder := struct{ F textfile.File }{F: f}

	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, nil))
	logger.Info("file read", "file", f, "holder", holder)
	marshalled, err := json.Marshal(holder)
	if err != nil {
		t.Fatalf("json.Marshal(holder) = %v, want nil", err)
	}

	outputs := map[string]string{
		"%v":           fmt.Sprintf("%v", f),
		"%+v":          fmt.Sprintf("%+v", f),
		"%#v":          fmt.Sprintf("%#v", f),
		"%s":           fmt.Sprintf("%s", f),
		"%x":           fmt.Sprintf("%x", f),
		"pointer %+v":  fmt.Sprintf("%+v", &f),
		"holder %+v":   fmt.Sprintf("%+v", holder),
		"String":       f.String(),
		"slog":         logged.String(),
		"json.Marshal": string(marshalled),
	}
	for name, got := range outputs {
		for _, leak := range []string{content, fmt.Sprintf("%x", content), "a2V5PWRldi1vbmx5"} {
			if strings.Contains(got, leak) {
				t.Errorf("%s of File = %q, want no file content", name, got)
			}
		}
	}
	if got := f.String(); !strings.Contains(got, "abc123") || !strings.Contains(got, "UTF-8") {
		t.Errorf("File.String() = %q, want the encoding and the hash", got)
	}
}
