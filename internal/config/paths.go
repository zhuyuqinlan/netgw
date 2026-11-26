// paths.go 集中管理跨平台配置文件路径。
//
// 统一约定：配置目录名称为 `netgw`。
//
//	Windows: %APPDATA%/netgw/config.yaml        （用户级；无系统级）
//	Linux:   ~/.config/netgw/config.yaml        （用户级，遵循 XDG）
//	Linux:   /etc/netgw/config.yaml             （系统级）
//	macOS:   ~/.config/netgw/config.yaml        （用户级）
//	macOS:   /etc/netgw/config.yaml             （系统级）
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// 配置目录名称（所有平台统一）。
const ConfigDirName = "netgw"

// ConfigFileName 配置文件名。
const ConfigFileName = "config.yaml"

// UserConfigPath 返回用户级配置文件路径。
// 无法确定（如 APPDATA/HOME 缺失）时返回空字符串。
func UserConfigPath() string {
	if runtime.GOOS == "windows" {
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			return ""
		}
		return filepath.Join(appdata, ConfigDirName, ConfigFileName)
	}
	// Unix：优先 XDG_CONFIG_HOME，其次 ~/.config。
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, ConfigDirName, ConfigFileName)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", ConfigDirName, ConfigFileName)
}

// SystemConfigPath 返回系统级配置文件路径。
// Windows 无系统级配置，返回空字符串。
func SystemConfigPath() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	return filepath.Join("/etc", ConfigDirName, ConfigFileName)
}

// LocalConfigPath 项目本地默认配置（当前工作目录下）。
func LocalConfigPath() string {
	return ConfigFileName
}

// Resolve 按优先级解析实际使用的配置文件路径：
//
//  1. 命令行显式指定的配置文件（必须存在，否则报错）；
//  2. 用户级配置文件（存在则使用）；
//  3. 系统级配置文件（仅当用户级不存在时读取）；
//  4. 项目默认配置 ./config.yaml（存在则使用）；
//  5. 均不存在时返回空路径（使用内置默认值）。
//
// 返回值：解析出的路径（可能为空）与来源说明。
func Resolve(explicit string) (path string, source string) {
	if explicit != "" {
		return explicit, "command line"
	}
	if p := UserConfigPath(); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, "user config"
		}
	}
	if p := SystemConfigPath(); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, "system config"
		}
	}
	if p := LocalConfigPath(); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, "local config.yaml"
		}
	}
	return "", "built-in defaults"
}

// LoadAuto 在 Load 的基础上按 Resolve 的优先级自动选择配置文件。
// 显式指定的配置文件不存在时报错（不得静默回退）；
// 配置存在但解析失败时向上返回错误。
func LoadAuto(explicit string) (*Config, string, error) {
	path, source := Resolve(explicit)
	if path == "" {
		cfg, err := Load("")
		if err != nil {
			return nil, source, err
		}
		return cfg, source, nil
	}
	cfg, err := Load(path)
	if err != nil {
		return nil, source, err
	}
	return cfg, source, nil
}

// EnsureUserConfig 在用户级配置不存在时写入默认配置模板。
// 已存在则不做任何修改（升级不覆盖用户配置）。
// 返回是否新建了配置文件。
func EnsureUserConfig(defaultYAML []byte) (bool, error) {
	path := UserConfigPath()
	if path == "" {
		return false, fmt.Errorf("cannot determine user config directory")
	}
	if _, err := os.Stat(path); err == nil {
		return false, nil // 已存在，不覆盖
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat config %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, defaultYAML, 0o644); err != nil {
		return false, fmt.Errorf("write config %s: %w", path, err)
	}
	return true, nil
}
