package analyzer

import (
	"context"
	"crypto/sha1"
	"embed"
	"fmt"
	"github.com/nxadm/tail"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

var writeSyncer = sync.Mutex{}

//go:embed *.gohtml
var tmplFS embed.FS

type Analyzer struct {
	Context               *context.Context
	FolderToWorkWith      string
	IsFolderTemp          bool
	fileWatchers          []*tail.Tail
	LastModifiedFileTime  time.Time
	DynamicEntities       DynamicEntities
	StaticEntities        []StaticEntity
	Filters               Filters
	OtherFiles            OtherFiles
	AggregatedLogs        Logs
	AggregatedThreadDumps AggregatedThreadDumps
	AggregatedStaticInfo  AggregatedStaticInfo
}
type StaticEntity struct {
	Name                string
	ConvertToStaticInfo func(path string) StaticInfo
	CheckPath           func(path string) bool
	CollectedInfo       StaticInfo
}
type DynamicEntities []DynamicEntity

//DynamicEntity is a type of log file. Every type is described in separate file of /entities/ folder.
//Every type has set of functions to convert logs of that type to unified logs of IntelliJ Log Analyzer
type DynamicEntity struct {
	Name                  string                             // Name of the Entity. For example "idea.log", "Thread dump", or "CPU snapshot". It will be used to group same entities.
	entityInstances       map[string]DynamicEntityProperties // entityInstances is path:DynamicEntityProperties map of every instance of entity created for every found path of this entity type.
	ConvertPathToLogs     func(path string) Logs             //ConvertPathToLogs represents file/folder to the array of log entries.
	ConvertStringToLogs   func(s string) (LogEntry, error)   //ConvertStringToLogs (should be defined for simple log files) represents a string as log entry. Needed when part of a log should be analyzed (for example during tailing process). Returns error if string does not fit log format.
	GetChangeablePath     func(path string) string           //GetChangeablePath (if defined) returns the part of given path, that should be monitored for changes. For simple log file, it is the log file itself. For reports (such as thread dumps) it is directory where new reports are being added
	CheckPath             func(path string) bool
	CheckIgnoredPath      func(path string) bool
	DefaultVisibility     func(path string) bool // DefaultVisibility is a function that returns true if this file should be checked in Filter (visible in "Summary" tab) by default.
	GetDisplayName        func(path string) string
	LineHighlightingColor string //Color represents the color that is used to highlight all lines of this entity type in the editor
}
type DynamicEntityProperties struct {
	Hash    string
	Visible bool
}

func (e *DynamicEntity) addDynamicEntityInstance(path string, visible bool) {
	if e.entityInstances == nil {
		e.entityInstances = make(map[string]DynamicEntityProperties)
	}
	e.entityInstances[path] = DynamicEntityProperties{
		Hash:    getHash(path),
		Visible: visible,
	}
}

//AddStaticEntity adds new static Entity to the list of known Entities. Should be Called within the application start.
func (a *Analyzer) AddStaticEntity(entity StaticEntity) {
	a.StaticEntities = append(a.StaticEntities, entity)
}

//AddDynamicEntity adds new dynamic Entity to the list of known Entities. Should be Called within the application start.
func (a *Analyzer) AddDynamicEntity(entity DynamicEntity) {
	a.DynamicEntities = append(a.DynamicEntities, entity)
}

//ParseLogDirectory analyzes provided path for known log elements
func (a *Analyzer) ParseLogDirectory(path string) {
	log.Printf("Parsing log directory %s", path)
	var wg sync.WaitGroup
	var collectedFiles []string
	visit := func(path string, file os.DirEntry, err error) error {
		wg.Add(1)
		go func() {
			defer wg.Done()
			isDynamic := a.CollectLogsFromDynamicEntities(path)
			isStatic := a.CollectStaticInfoFromStaticEntities(path)
			writeSyncer.Lock()
			if isStatic || isDynamic {
				collectedFiles = append(collectedFiles, path)
			} else {
				if !file.IsDir() && !IsHiddenFile(filepath.Base(path)) {
					a.OtherFiles.Append(path)
				}
			}
			writeSyncer.Unlock()
		}()
		return nil
	}
	_ = filepath.WalkDir(path, visit)
	wg.Wait()
	a.OtherFiles = a.OtherFiles.FilterAnalyzedDirectories(collectedFiles)
}

func (a *Analyzer) GetLastModifiedFile() time.Time {
	if !a.LastModifiedFileTime.IsZero() {
		return a.LastModifiedFileTime
	}
	var rememberedPath = ""
	visit := func(path string, file os.DirEntry, err error) error {
		if !file.IsDir() && !IsHiddenFile(filepath.Base(path)) {
			if GetFileModTime(path).After(a.LastModifiedFileTime) {
				a.LastModifiedFileTime = GetFileModTime(path)
				rememberedPath = path
			}
		}
		return nil
	}
	_ = filepath.WalkDir(a.FolderToWorkWith, visit)
	log.Printf("Last modified file: %s timestamp: %s", rememberedPath, a.LastModifiedFileTime)
	return a.LastModifiedFileTime
}

//IsEmpty checks if config has at least one filled attribute
func (a *Analyzer) IsEmpty() bool {
	return reflect.ValueOf(*a).IsZero()
}

func (a *Analyzer) GetLogs() *Logs {
	if !a.AggregatedLogs.IsEmpty() {
		return &a.AggregatedLogs
	}
	return nil
}

func (a *Analyzer) GetStaticInfo() *AggregatedStaticInfo {
	if !(len(a.AggregatedStaticInfo) == 0) {
		return &a.AggregatedStaticInfo
	}
	a.AggregatedStaticInfo = aggregateStaticInfo(a.StaticEntities)
	return a.GetStaticInfo()
}
func (a *Analyzer) GetOtherFiles() *OtherFiles {
	if !a.AggregatedLogs.IsEmpty() {
		return &a.OtherFiles
	}
	return nil
}

//GetThreadDump returns Analyzed ThreadDumps folder as AggregatedThreadDumps entity. Analyzes it if it was not done already.
func (a *Analyzer) GetThreadDump(threadDumpsFolder string) *ThreadDump {
	t := a.AggregatedThreadDumps[threadDumpsFolder]
	if t != nil {
		return &t
	}
	a.AggregatedThreadDumps[threadDumpsFolder] = make(ThreadDump)
	a.AggregatedThreadDumps[threadDumpsFolder] = analyzeThreadDumpsFolder(a.FolderToWorkWith, threadDumpsFolder)
	return a.GetThreadDump(threadDumpsFolder)
}
func (a *Analyzer) GetFilters() *Filters {
	if !a.Filters.IsEmpty() {
		return &a.Filters
	}
	return nil
}

func (a *Analyzer) CollectStaticInfoFromStaticEntities(path string) (analyzed bool) {
	analyzed = false
	for i, entity := range a.StaticEntities {
		if entity.CheckPath(path) == true {
			a.StaticEntities[i].CollectedInfo = entity.ConvertToStaticInfo(path)
			analyzed = true
		}
	}
	return analyzed
}

// CollectLogsFromDynamicEntities Checks if path fulfil the Entity requirements and Adds all the Entity's logEntries to the aggregated logs
func (a *Analyzer) CollectLogsFromDynamicEntities(path string) (analyzed bool) {
	analyzed = false
	for i, entity := range a.DynamicEntities {
		if entity.CheckIgnoredPath != nil {
			if entity.CheckIgnoredPath(path) == true {
				return true
			}
		}
		if entity.CheckPath(path) == true {
			logEntries := entity.ConvertPathToLogs(path)
			if logEntries == nil {
				log.Printf("Entity \"%s\" returned nothing for %s. Adding file to other files", entity.Name, path)
			} else {
				if entity.DefaultVisibility == nil {
					entity.DefaultVisibility = func(path string) bool {
						return true
					}
				}
				writeSyncer.Lock()
				a.DynamicEntities[i].addDynamicEntityInstance(path, entity.DefaultVisibility(path))
				a.AggregatedLogs.AppendSeveral(a.DynamicEntities[i].Name, a.DynamicEntities[i].entityInstances[path], logEntries)
				writeSyncer.Unlock()
				analyzed = true
			}
		}
	}
	return analyzed
}

// GenerateFilters Generates filters for all Entities and saves them into Filters slice
func (a *Analyzer) GenerateFilters() {
	filter := a.InitFilter()
	for _, entity := range a.DynamicEntities {
		for path, _ := range entity.entityInstances {
			filter.Append(entity, path)
		}
	}
	filter.SortByFilename()
}

func (a *Analyzer) InitFilter() *Filters {
	a.Filters = make(Filters)
	return &a.Filters
}

func (a *Analyzer) Clear() {
	a.AggregatedLogs = Logs{}
	a.Filters = Filters{}
	a.OtherFiles = OtherFiles{}
	a.AggregatedStaticInfo = AggregatedStaticInfo{}
	a.AggregatedThreadDumps = AggregatedThreadDumps{}
	a.LastModifiedFileTime = time.Time{}
	for i, _ := range a.StaticEntities {
		a.StaticEntities[i].CollectedInfo = StaticInfo{}
	}
	for i, _ := range a.DynamicEntities {
		a.DynamicEntities[i].entityInstances = make(map[string]DynamicEntityProperties)
	}
	if a.IsFolderTemp {
		err := os.RemoveAll(a.FolderToWorkWith)
		if err != nil {
			log.Printf("Removing folder '%s' failed. Error: %s", a.FolderToWorkWith, err)
		} else {
			log.Printf("Temp folder %s removed", a.FolderToWorkWith)
		}
	}
	a.IsFolderTemp = false
	for _, watcher := range a.fileWatchers {
		if watcher != nil {
			watcher.Stop()
		}
	}
	a.fileWatchers = nil
}

func (a *Analyzer) GetThreadDumps(dir string) Logs {
	for _, entity := range a.DynamicEntities {
		for path, _ := range entity.entityInstances {
			if strings.Contains(path, dir) {
				return entity.ConvertPathToLogs(path)
			}
		}
	}
	return nil
}

func (e *DynamicEntities) GetInstanceByID(id string) *DynamicEntityProperties {
	for _, dynamicEntity := range *e {
		for _, entityInstance := range dynamicEntity.entityInstances {
			if id == entityInstance.Hash {
				return &entityInstance
			}
		}
	}
	return nil
}

func aggregateStaticInfo(entity []StaticEntity) (a AggregatedStaticInfo) {
	a = make(AggregatedStaticInfo)
	for _, staticEntity := range entity {
		a[staticEntity.Name] = staticEntity.CollectedInfo
	}
	return a
}

func getHash(s string) string {
	h := sha1.New()
	h.Write([]byte(s))
	bs := h.Sum(nil)
	sh := fmt.Sprintf("%x\n", bs)
	return sh
}

/**
 * Parses string s with the given regular expression and returns the
 * group values defined in the expression.
 *
 */
func GetRegexNamedCapturedGroups(regEx, s string) (paramsMap map[string]string) {

	var compRegEx = regexp.MustCompile(regEx)
	match := compRegEx.FindStringSubmatch(s)
	paramsMap = make(map[string]string)

	for i, name := range compRegEx.SubexpNames() {
		if i > 0 && i <= len(match) {
			paramsMap[name] = match[i]
		}
	}
	return paramsMap
}

func sortedKeys[K string, V any](m map[K]V) []K {
	keys := make([]K, len(m))
	i := 0
	for k := range m {
		keys[i] = k
		i++
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func SliceContains[S comparable](slice []S, element S) int {
	for i, e := range slice {
		if e == element {
			return i
		}
	}
	return -1
}

func IsHiddenFile(filename string) bool {
	if runtime.GOOS != "windows" {
		return filename[0:1] == "."
	}
	return false
}

func GetFileModTime(path string) (date time.Time) {
	fileinfo, err := os.Stat(path)
	if err == nil {
		return fileinfo.ModTime()
	}
	return time.Time{}
}
