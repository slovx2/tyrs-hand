package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplicitEnv(t *testing.T) {
	t.Setenv("JEV_API_KEY", "test-environment")
	path := filepath.Join(t.TempDir(), "config.env")
	if err := os.WriteFile(path, []byte("# 测试配置\nJEV_API_KEY='test-file'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	key, err := readAPIKey(path)
	if err != nil || key != "test-file" {
		t.Fatal("指定 env 未生效")
	}
	key, err = readAPIKey("")
	if err != nil || key != "test-environment" {
		t.Fatal("环境变量未生效")
	}
	if err := os.WriteFile(path, []byte("JEV_API_KEY='test-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = readAPIKey(path)
	if err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatal("配置解析错误包含密钥或未报错")
	}
}
