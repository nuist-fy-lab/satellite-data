package era5

import (
	"errors"
	"io"
	"os"
	"strings"
)

func tokenFromFile() (string, error) {
	filename := strings.TrimSpace(os.Getenv("CDS_API_TOKEN_FILE"))
	if filename == "" {
		return "", ErrCredentialsRequired
	}
	f, err := os.Open(filename)
	if err != nil {
		return "", ErrCredentialsRequired
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("CDS token file must be a private regular file (0600 or 0400)")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return "", errors.New("invalid CDS token file")
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", ErrCredentialsRequired
	}
	if strings.ContainsAny(token, "\r\n\t ") {
		return "", errors.New("invalid CDS token file")
	}
	return token, nil
}
