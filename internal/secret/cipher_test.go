package secret

import (
	"bytes"
	"testing"

	"github.com/santinomarial/glider/internal/api"
)

func TestEnvelopeRoundTripUsesFreshNonceAndAuthenticatesContext(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	cipher, _ := NewCipher(key, "cluster-a")
	value := api.Secret{Metadata: api.Metadata{ID: "database"}, Data: map[string][]byte{"password": []byte("correct horse")}}
	one, err := cipher.Encrypt(value)
	if err != nil {
		t.Fatal(err)
	}
	two, _ := cipher.Encrypt(value)
	if bytes.Equal(one.Nonce, two.Nonce) || bytes.Contains(one.Ciphertext, value.Data["password"]) {
		t.Fatal("envelope reused nonce or exposed plaintext")
	}
	got, err := cipher.Decrypt(one)
	if err != nil || !bytes.Equal(got.Data["password"], value.Data["password"]) {
		t.Fatalf("decrypt = %#v, %v", got, err)
	}
	other, _ := NewCipher(key, "cluster-b")
	if _, err := other.Decrypt(one); err == nil {
		t.Fatal("envelope decrypted in another cluster context")
	}
	one.Ciphertext[0] ^= 1
	if _, err := cipher.Decrypt(one); err == nil {
		t.Fatal("tampered envelope decrypted")
	}
}
