package settings

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

const builtinSkillsRoot = "skills"

//go:embed skills/tyrs-browser/SKILL.md skills/tyrs-browser/agents/openai.yaml
var builtinSkills embed.FS

var (
	builtinSkillsRevisionOnce sync.Once
	builtinSkillsRevision     string
)

// InstallBuiltinSkills installs the Worker-managed skills into a Codex home.
func InstallBuiltinSkills(codexHome string) error {
	return fs.WalkDir(builtinSkills, builtinSkillsRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(builtinSkillsRoot, filepath.FromSlash(path))
		if err != nil {
			return err
		}
		target := filepath.Join(codexHome, builtinSkillsRoot, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := builtinSkills.ReadFile(path)
		if err != nil {
			return err
		}
		return writeAtomicFile(target, data, 0o600)
	})
}

// BuiltinSkillsRevision returns a content-derived revision for Worker installation checks.
func BuiltinSkillsRevision() string {
	builtinSkillsRevisionOnce.Do(func() {
		digest := sha256.New()
		_ = fs.WalkDir(builtinSkills, builtinSkillsRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			data, err := builtinSkills.ReadFile(path)
			if err != nil {
				return err
			}
			_, _ = digest.Write([]byte(path))
			_, _ = digest.Write([]byte{0})
			_, _ = digest.Write(data)
			_, _ = digest.Write([]byte{0})
			return nil
		})
		builtinSkillsRevision = hex.EncodeToString(digest.Sum(nil))
	})
	return builtinSkillsRevision
}

func writeAtomicFile(path string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tyrs-skill-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
