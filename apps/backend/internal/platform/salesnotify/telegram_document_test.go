package salesnotify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramSender_SendDocumentUploadsTheFileAsMultipart(t *testing.T) {
	var (
		path, chat, caption, fileName, fileType string
		file                                    []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("not multipart: %v", err)
			return
		}
		chat, caption = r.FormValue("chat_id"), r.FormValue("caption")
		f, h, err := r.FormFile("document")
		if err != nil {
			t.Errorf("no document part: %v", err)
			return
		}
		defer func() { _ = f.Close() }()
		file, _ = io.ReadAll(f)
		fileName, fileType = h.Filename, h.Header.Get("Content-Type")
		if chat == "-9" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":403,"description":"Forbidden: bot was kicked"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()
	s := NewTelegramSender("SECRET", srv.URL)

	body := []byte("PK\x03\x04 not really a workbook")
	err := s.SendDocument(context.Background(), "-1", `guests "x"`+"\r\n.xlsx", "application/vnd.test", body, "Show, 24.12.2026")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/botSECRET/sendDocument" || chat != "-1" || caption != "Show, 24.12.2026" ||
		string(file) != string(body) || fileType != "application/vnd.test" {
		t.Errorf("request = %q chat=%q caption=%q type=%q file=%q", path, chat, caption, fileType, file)
	}
	if strings.ContainsAny(fileName, "\"\r\n") {
		t.Errorf("file name %q can break out of the header", fileName)
	}

	// A refusal is a PermanentError that carries neither the token nor the file.
	err = s.SendDocument(context.Background(), "-9", "g.xlsx", "", body, "")
	var perm *PermanentError
	if !errors.As(err, &perm) || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("err = %v, want a PermanentError without the token", err)
	}
}
