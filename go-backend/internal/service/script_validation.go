package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"path/filepath"
	"strings"
)

var pdfSignature = []byte("%PDF-")

// ValidatePDFUpload verifies the metadata and signature of an uploaded PDF.
// The file cursor is reset to the beginning before the function returns.
func ValidatePDFUpload(file multipart.File, header *multipart.FileHeader, maxUploadSize int64) error {
	if file == nil || header == nil {
		return ErrScriptFileRequired
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("%w: seek uploaded file: %v", ErrInternal, err)
	}
	if maxUploadSize <= 0 {
		return fmt.Errorf("%w: max upload size must be positive", ErrInternal)
	}
	if header.Size > maxUploadSize {
		return ErrScriptFileTooLarge
	}
	if !strings.EqualFold(filepath.Ext(header.Filename), ".pdf") {
		return ErrScriptFileExtension
	}

	contentType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(contentType, "application/pdf") {
		return ErrScriptFileContentType
	}

	signature := make([]byte, len(pdfSignature))
	_, readErr := io.ReadFull(file, signature)
	_, seekErr := file.Seek(0, io.SeekStart)
	if seekErr != nil {
		return fmt.Errorf("%w: reset uploaded file: %v", ErrInternal, seekErr)
	}
	if readErr != nil || !bytes.Equal(signature, pdfSignature) {
		return ErrScriptFileSignature
	}

	return nil
}

// IsScriptFileValidationError reports whether err is safe to return as a
// client-side upload validation failure.
func IsScriptFileValidationError(err error) bool {
	return errors.Is(err, ErrScriptFileRequired) ||
		errors.Is(err, ErrScriptFileTooLarge) ||
		errors.Is(err, ErrScriptFileExtension) ||
		errors.Is(err, ErrScriptFileContentType) ||
		errors.Is(err, ErrScriptFileSignature)
}
