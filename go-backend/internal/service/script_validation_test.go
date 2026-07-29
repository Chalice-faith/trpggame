package service

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/textproto"
	"testing"
)

type memoryMultipartFile struct {
	*bytes.Reader
}

func (f *memoryMultipartFile) Close() error {
	return nil
}

func newPDFUpload(filename, contentType string, content []byte) (*memoryMultipartFile, *multipart.FileHeader) {
	headers := make(textproto.MIMEHeader)
	headers.Set("Content-Type", contentType)

	return &memoryMultipartFile{Reader: bytes.NewReader(content)}, &multipart.FileHeader{
		Filename: filename,
		Header:   headers,
		Size:     int64(len(content)),
	}
}

func TestValidatePDFUpload(t *testing.T) {
	validPDF := []byte("%PDF-1.7\nsample")

	tests := []struct {
		name          string
		filename      string
		contentType   string
		content       []byte
		maxUploadSize int64
		wantErr       error
	}{
		{
			name:          "valid PDF",
			filename:      "adventure.pdf",
			contentType:   "application/pdf",
			content:       validPDF,
			maxUploadSize: int64(len(validPDF)),
		},
		{
			name:          "uppercase extension and MIME parameters",
			filename:      "ADVENTURE.PDF",
			contentType:   "application/pdf; charset=binary",
			content:       validPDF,
			maxUploadSize: 1024,
		},
		{
			name:          "file too large",
			filename:      "adventure.pdf",
			contentType:   "application/pdf",
			content:       validPDF,
			maxUploadSize: int64(len(validPDF) - 1),
			wantErr:       ErrScriptFileTooLarge,
		},
		{
			name:          "wrong extension",
			filename:      "adventure.pdf.exe",
			contentType:   "application/pdf",
			content:       validPDF,
			maxUploadSize: 1024,
			wantErr:       ErrScriptFileExtension,
		},
		{
			name:          "wrong content type",
			filename:      "adventure.pdf",
			contentType:   "application/octet-stream",
			content:       validPDF,
			maxUploadSize: 1024,
			wantErr:       ErrScriptFileContentType,
		},
		{
			name:          "invalid content type",
			filename:      "adventure.pdf",
			contentType:   "application/pdf; charset",
			content:       validPDF,
			maxUploadSize: 1024,
			wantErr:       ErrScriptFileContentType,
		},
		{
			name:          "invalid PDF signature",
			filename:      "adventure.pdf",
			contentType:   "application/pdf",
			content:       []byte("not a PDF"),
			maxUploadSize: 1024,
			wantErr:       ErrScriptFileSignature,
		},
		{
			name:          "truncated PDF signature",
			filename:      "adventure.pdf",
			contentType:   "application/pdf",
			content:       []byte("%PDF"),
			maxUploadSize: 1024,
			wantErr:       ErrScriptFileSignature,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, header := newPDFUpload(tt.filename, tt.contentType, tt.content)
			if _, err := file.Seek(2, io.SeekStart); err != nil {
				t.Fatalf("prepare file position: %v", err)
			}

			err := ValidatePDFUpload(file, header, tt.maxUploadSize)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidatePDFUpload() error = %v, want %v", err, tt.wantErr)
			}

			position, seekErr := file.Seek(0, io.SeekCurrent)
			if seekErr != nil {
				t.Fatalf("read file position: %v", seekErr)
			}
			if position != 0 {
				t.Fatalf("file position = %d, want 0", position)
			}
		})
	}
}

func TestValidatePDFUploadRequiresFile(t *testing.T) {
	if err := ValidatePDFUpload(nil, nil, 1024); !errors.Is(err, ErrScriptFileRequired) {
		t.Fatalf("ValidatePDFUpload() error = %v, want %v", err, ErrScriptFileRequired)
	}
}

func TestValidatePDFUploadRejectsInvalidLimit(t *testing.T) {
	file, header := newPDFUpload("adventure.pdf", "application/pdf", []byte("%PDF-1.7"))

	err := ValidatePDFUpload(file, header, 0)
	if !errors.Is(err, ErrInternal) {
		t.Fatalf("ValidatePDFUpload() error = %v, want wrapped %v", err, ErrInternal)
	}
}

func TestIsScriptFileValidationError(t *testing.T) {
	if !IsScriptFileValidationError(ErrScriptFileSignature) {
		t.Fatal("expected script signature error to be classified as validation error")
	}
	if IsScriptFileValidationError(ErrInternal) {
		t.Fatal("did not expect internal error to be classified as validation error")
	}
}
