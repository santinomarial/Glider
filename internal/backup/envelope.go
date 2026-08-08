// Package backup encrypts and authenticates etcd snapshots at rest.
package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

var header = []byte("GLIDERB1")

func LoadKey(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("backup key must not be accessible by group or others")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 128 {
		decoded, decodeErr := hex.DecodeString(string(data))
		if decodeErr == nil {
			data = decoded
		}
	}
	if len(data) != 64 {
		return nil, errors.New("backup key must contain exactly 64 raw bytes or 128 hexadecimal characters")
	}
	return data, nil
}
func Encrypt(dst io.Writer, src io.Reader, key []byte) error {
	if len(key) != 64 {
		return errors.New("invalid key length")
	}
	block, err := aes.NewCipher(key[:32])
	if err != nil {
		return err
	}
	iv := make([]byte, aes.BlockSize)
	if _, err = io.ReadFull(rand.Reader, iv); err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key[32:])
	multi := io.MultiWriter(dst, mac)
	if _, err = multi.Write(header); err != nil {
		return err
	}
	if _, err = multi.Write(iv); err != nil {
		return err
	}
	stream := cipher.NewCTR(block, iv)
	_, err = io.Copy(&cipher.StreamWriter{S: stream, W: multi}, src)
	if err != nil {
		return err
	}
	_, err = dst.Write(mac.Sum(nil))
	return err
}
func Decrypt(dst io.Writer, source *os.File, key []byte) error {
	if len(key) != 64 {
		return errors.New("invalid key length")
	}
	info, err := source.Stat()
	if err != nil {
		return err
	}
	minimum := int64(len(header) + aes.BlockSize + sha256.Size)
	if info.Size() < minimum {
		return errors.New("backup envelope is truncated")
	}
	payloadSize := info.Size() - sha256.Size
	mac := hmac.New(sha256.New, key[32:])
	if _, err = source.Seek(0, 0); err != nil {
		return err
	}
	if _, err = io.CopyN(mac, source, payloadSize); err != nil {
		return err
	}
	stored := make([]byte, sha256.Size)
	if _, err = io.ReadFull(source, stored); err != nil {
		return err
	}
	if !hmac.Equal(mac.Sum(nil), stored) {
		return errors.New("backup authentication failed")
	}
	if _, err = source.Seek(0, 0); err != nil {
		return err
	}
	prefix := make([]byte, len(header)+aes.BlockSize)
	if _, err = io.ReadFull(source, prefix); err != nil {
		return err
	}
	if !hmac.Equal(prefix[:len(header)], header) {
		return errors.New("unsupported backup envelope")
	}
	block, err := aes.NewCipher(key[:32])
	if err != nil {
		return err
	}
	reader := &cipher.StreamReader{S: cipher.NewCTR(block, prefix[len(header):]), R: io.LimitReader(source, payloadSize-int64(len(prefix)))}
	_, err = io.Copy(dst, reader)
	return err
}
