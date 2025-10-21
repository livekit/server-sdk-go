package cloudagents

import (
	"errors"
	"io/fs"

	"github.com/pelletier/go-toml"
)

type ProjectType string

const (
	ProjectTypePythonPip ProjectType = "python.pip"
	ProjectTypePythonUV  ProjectType = "python.uv"
	ProjectTypeNode      ProjectType = "node"
	ProjectTypeUnknown   ProjectType = "unknown"
)

func (p ProjectType) IsPython() bool {
	return p == ProjectTypePythonPip || p == ProjectTypePythonUV
}

func (p ProjectType) IsNode() bool {
	return p == ProjectTypeNode
}

func (p ProjectType) Lang() string {
	switch {
	case p.IsPython():
		return "Python"
	case p.IsNode():
		return "Node.js"
	default:
		return ""
	}
}

func (p ProjectType) FileExt() string {
	switch {
	case p.IsPython():
		return ".py"
	case p.IsNode():
		return ".js"
	default:
		return ""
	}
}

func (p ProjectType) DefaultEntrypoint() string {
	switch {
	case p.IsPython():
		return "agent.py"
	case p.IsNode():
		return "agent.js"
	default:
		return ""
	}
}

func detectProjectType(dir fs.FS) (ProjectType, error) {
	// Node.js detection
	if fileExists(dir, "package.json") {
		return ProjectTypeNode, nil
	}

	// Python detection
	if fileExists(dir, "uv.lock") {
		return ProjectTypePythonUV, nil
	}
	if fileExists(dir, "poetry.lock") || fileExists(dir, "Pipfile.lock") {
		return ProjectTypePythonPip, nil // We can treat as pip-compatible
	}
	if fileExists(dir, "requirements.txt") {
		return ProjectTypePythonPip, nil
	}
	if fileExists(dir, "pyproject.toml") {
		data, err := fs.ReadFile(dir, "pyproject.toml")
		if err == nil {
			var doc map[string]any
			if err := toml.Unmarshal(data, &doc); err == nil {
				if tool, ok := doc["tool"].(map[string]any); ok {
					if _, hasPoetry := tool["poetry"]; hasPoetry {
						return ProjectTypePythonPip, nil
					}
					if _, hasPdm := tool["pdm"]; hasPdm {
						return ProjectTypePythonPip, nil
					}
					if _, hasHatch := tool["hatch"]; hasHatch {
						return ProjectTypePythonPip, nil
					}
					if _, hasUv := tool["uv"]; hasUv {
						return ProjectTypePythonUV, nil
					}
				}
			}
		}
		// Default to pip if pyproject.toml is present but not informative
		return ProjectTypePythonPip, nil
	}

	return ProjectTypeUnknown, errors.New("project type could not be identified; expected package.json, requirements.txt, pyproject.toml, or lock files")
}

func fileExists(dir fs.FS, filename string) bool {
	_, err := fs.Stat(dir, filename)
	return err == nil
}
