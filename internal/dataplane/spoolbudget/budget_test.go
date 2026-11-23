package spoolbudget

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBothWritersShareOneBudget(t *testing.T) {
	mount := t.TempDir()
	left, right := filepath.Join(mount, "left"), filepath.Join(mount, "right")
	for _, dir := range []string{left, right} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configFile := filepath.Join(t.TempDir(), "budget.json")
	data, err := json.Marshal(config{Mount: mount, Roots: []string{left, right}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(configEnv, configFile)
	a, err := os.OpenFile(filepath.Join(left, "a.part"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := os.OpenFile(filepath.Join(right, "b.part"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	wrappedA, err := Wrap(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	wrappedB, err := Wrap(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	block := wrappedA.(*writer).block
	ceiling := limit / block * block
	if err := a.Truncate(ceiling - 2*block); err != nil {
		t.Fatal(err)
	}
	if _, err := wrappedA.Write(make([]byte, block)); err != nil {
		t.Fatal(err)
	}
	if _, err := wrappedB.Write(make([]byte, block)); err != nil {
		t.Fatal(err)
	}
	if n, err := wrappedB.Write([]byte{1}); n != 0 || !errors.Is(err, ErrFull) {
		t.Fatalf("write past the common limit = %d, %v", n, err)
	}
	info, err := b.Stat()
	if err != nil || info.Size() != block {
		t.Fatalf("partial file changed after deferral: %v, %v", info, err)
	}
}
