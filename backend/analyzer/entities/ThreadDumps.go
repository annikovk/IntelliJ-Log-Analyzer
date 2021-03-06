package entities

import (
	"log_analyzer/backend/analyzer"
	"os"
	"path/filepath"
	"strings"
)

func init() {
	CurrentAnalyzer.AddDynamicEntity(analyzer.DynamicEntity{
		Name:                  "Thread Dumps",
		ConvertPathToLogs:     getLogEntry,
		CheckPath:             isThreadDump,
		GetDisplayName:        analyzer.GetThreadDumpDisplayName,
		LineHighlightingColor: "#faa379",
	})
}
func isThreadDump(path string) bool {
	if strings.Contains(path, "threadDump") {
		fileInfo, _ := os.Stat(path)
		if fileInfo.IsDir() {
			return true
		}
	}
	return false
}

//getLogEntry represents ThreadDump folder as a Log entry.
func getLogEntry(path string) analyzer.Logs {
	logToPass := []analyzer.LogEntry{}
	fileName := filepath.Base(path)
	logToPass = append(logToPass, analyzer.LogEntry{
		Severity: "FREEZE",
		Time:     analyzer.GetTimeStampFromThreadDump(path),
		Text:     "Freeze started: " + fileName,
	})
	return logToPass
}
