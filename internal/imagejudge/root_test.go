package imagejudge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectRootFromSubdirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/check\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("OLLAMA_PORT=12345\nOLLAMA_MODEL=test-vision\n"), 0600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "nested")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(child); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	c, err := LoadConfig("")
	if err != nil || c.OllamaModel != "test-vision" {
		t.Fatalf("子目录配置发现失败：%v", err)
	}
}
