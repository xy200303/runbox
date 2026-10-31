package runner

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func echoCommand(text string) string {
	if runtime.GOOS == "windows" {
		return "echo " + text
	}
	return "printf '%s\\n' " + shellQuote(text)
}

func writeCommand(relPath, content string) string {
	if runtime.GOOS == "windows" {
		return "echo " + content + "> " + filepath.FromSlash(relPath)
	}
	return "printf '%s' " + shellQuote(content) + " > " + relPath
}

func catCommand(relPath string) string {
	if runtime.GOOS == "windows" {
		return "type " + filepath.FromSlash(relPath)
	}
	return "cat " + shellQuote(relPath)
}

func exitCommand(code int) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("exit /b %d", code)
	}
	return fmt.Sprintf("exit %d", code)
}

func stderrCommand(text string) string {
	if runtime.GOOS == "windows" {
		return "echo " + text + " 1>&2"
	}
	return "echo " + shellQuote(text) + " >&2"
}

func sleepCommand() string {
	if runtime.GOOS == "windows" {
		return "ping -n 6 127.0.0.1 >nul"
	}
	return "sleep 5"
}

func nestedOutputCommand() string {
	if runtime.GOOS == "windows" {
		return `if not exist out\sub mkdir out\sub & echo nested> out\sub\n.txt`
	}
	return `mkdir -p out/sub && printf 'nested' > out/sub/n.txt`
}

func twoOutputsCommand() string {
	if runtime.GOOS == "windows" {
		return `echo a> out\a.txt & echo b> out\b.txt`
	}
	return `printf a > out/a.txt; printf b > out/b.txt`
}
