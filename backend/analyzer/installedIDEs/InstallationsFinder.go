package installedIDEs

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type ideInfoFromDebugger struct {
	Name        string `json:"name"`
	ProductName string `json:"productName"`
	BuildNumber string `json:"buildNumber"`
}

type IdeInfo struct {
	Name              string
	Version           string
	BuildNumber       string
	ProductCode       string
	DataDirectoryName string
	LogsDirectory     string
	IsRepairBundled   bool
	Launch            []struct {
		Os                 string `json:"os"`
		LauncherPath       string `json:"launcherPath"`
		JavaExecutablePath string `json:"javaExecutablePath"`
		VmOptionsFilePath  string `json:"vmOptionsFilePath"`
	} `json:"launch"`
}
type IDE struct {
	Binary  string
	Package string
	Running bool
	Info    IdeInfo
}

var (
	IdePropertiesMap                        = map[string]string{}
	IdeProductInfoRelatedToInstallationPath = map[string]string{
		"darwin":  "/Contents/Resources/product-info.json",
		"linux":   "/product-info.json",
		"windows": "/product-info.json",
	}
	possibleBaseFileNames              = []string{"appcode", "clion", "datagrip", "dataspell", "goland", "idea", "phpstorm", "pycharm", "rubymine", "webstorm", "rider", "Draft"}
	IdeBinaryRelatedToInstallationPath = map[string]string{
		"darwin":  "/Contents/MacOS/{possibleBaseFileName}",
		"linux":   "/bin/{possibleBaseFileName}.sh",
		"windows": "/bin/{possibleBaseFileName}64.exe",
	}
	possibleBinariesPaths = map[string][]string{
		"darwin":  {"/Applications/*.app/Contents/MacOS/{possibleBaseFileName}", "$HOME/Library/Application Support/JetBrains/Toolbox/apps/*/ch-*/*/*.app/Contents/MacOS/{possibleBaseFileName}"},
		"linux":   {"$HOME/.local/share/JetBrains/Toolbox/apps/*/ch-*/*/bin/{possibleBaseFileName}.sh"},
		"windows": {os.Getenv("HOMEDRIVE") + "/Program Files/JetBrains/*" + IdeBinaryRelatedToInstallationPath["windows"], os.Getenv("LOCALAPPDATA") + "/JetBrains/Toolbox/apps/*/ch-*/*" + IdeBinaryRelatedToInstallationPath["windows"]},
	}
	defaultLogsDirLocation = map[string]string{
		"darwin":  UserHomeDir() + "/Library/Logs/JetBrains/{dataDirectoryName}/",
		"linux":   UserHomeDir() + "/.cache/JetBrains/{dataDirectoryName}/log/",
		"windows": os.Getenv("LOCALAPPDATA") + "/JetBrains/{dataDirectoryName}/log/",
	}
	possibleIdeaPropertiesFileLocations = map[string][]string{
		"darwin":  {"${IDE_BasefileName}_PROPERTIES", UserHomeDir() + "/Library/Application Support/JetBrains/{dataDirectoryName}/idea.properties", UserHomeDir() + "/idea.properties", "{ideaPackage}/Contents/bin/idea.properties"},
		"linux":   {"${IDE_BasefileName}_PROPERTIES", UserHomeDir() + "/.config/JetBrains/{dataDirectoryName}/idea.properties", UserHomeDir() + "/idea.properties", "{ideaPackage}/bin/idea.properties"},
		"windows": {"${IDE_BasefileName}_PROPERTIES", defaultSystemDirLocation[runtime.GOOS] + "/idea.properties", UserHomeDir() + "/idea.properties", "{ideaPackage}/bin/idea.properties"},
	}
	defaultSystemDirLocation = map[string]string{
		"darwin":  "${HOME}/Library/Caches/JetBrains/{dataDirectoryName}/",
		"linux":   "${HOME}/.cache/JetBrains/{dataDirectoryName}/",
		"windows": os.Getenv("LOCALAPPDATA") + "/JetBrains/{dataDirectoryName}/",
	}
)

func GetIdeInstallations() (ides []IDE) {
	runningIDEs := getRunningIdes()
	log.Printf("Scanning system for IDE installations")
	var installedIdes []string
	installedIdes, _ = findInstalledIdePackages()

	for _, idePackage := range installedIdes {
		info, _ := getIdeInfoByPackage(idePackage)
		binary, _ := getIdeBinaryByPackage(idePackage)
		info.LogsDirectory = getIdeLogsDir(binary)
		isRunning := checkIfInstallationRunning(runningIDEs, info)
		if info.LogsDirectory != "" {
			ides = append(ides, IDE{
				Binary:  binary,
				Package: idePackage,
				Running: isRunning,
				Info:    info,
			})
			//log.Printf("[runnning: %v] [%v] %v %v (%v-%v) - %v \n", isRunning, i, info.Name, info.Version, info.ProductCode, info.BuildNumber, beautifyPackageName(idePackage))
		}
	}
	//sort them by running state. Running ones first
	sort.Slice(ides, func(i int, j int) bool {
		return ides[i].Running
	})
	return ides
}

func checkIfInstallationRunning(runningIDEs []ideInfoFromDebugger, info IdeInfo) bool {
	for _, e := range runningIDEs {
		if e.BuildNumber == info.BuildNumber && strings.Contains(info.Name, e.ProductName) {
			return true
		}
	}
	return false
}

func getRunningIdes() (ides []ideInfoFromDebugger) {
	var wg sync.WaitGroup
	for i := 63342; i < 63392; i++ {
		wg.Add(1)
		go func(i int) {
			url := fmt.Sprintf("http://localhost:%d/api/about", i)
			if ideInfo, err := getIdeInfoFromPort(url); err == nil {
				ides = append(ides, ideInfo)
			}
			defer wg.Done()
		}(i)
	}
	wg.Wait()
	return ides
}

func getIdeInfoFromPort(url string) (ideInfoFromDebugger, error) {
	client := http.Client{
		Timeout: time.Second,
	}
	res, err := client.Get(url)
	if err != nil {
		//log.Printf("Error getting HTTP response. err: %s", err.Error())
		return ideInfoFromDebugger{}, err
	}
	var ideInstance ideInfoFromDebugger
	content, _ := ioutil.ReadAll(res.Body)
	_ = res.Body.Close()
	ideInstance = parseRunningIdeInfo(content)
	log.Printf("Found running IDE %v at url %v", ideInstance, url)
	return ideInstance, err
}

func parseRunningIdeInfo(body []byte) ideInfoFromDebugger {
	ideInfo := ideInfoFromDebugger{}
	jsonErr := json.Unmarshal(body, &ideInfo)
	if jsonErr != nil {
		log.Printf("Could not unmarshall JSON, %s", jsonErr.Error())
	}
	return ideInfo
}

func findInstalledIdePackages() (installedIdes []string, err error) {
	for _, path := range getOsDependentDir(possibleBinariesPaths) {
		var foundInstallations []string
		foundInstallations, err = findIdeInstallationsByMask(path)
		installedIdes = append(installedIdes, foundInstallations...)
	}
	return installedIdes, err
}

func getOsDependentDir(fromVariable map[string][]string) []string {
	if len(fromVariable[runtime.GOOS]) > 0 {
		return fromVariable[runtime.GOOS]
	}
	log.Printf("This OS is not yet supported")
	return nil
}

func findIdeInstallationsByMask(path string) (foundIdePackages []string, err error) {
	for _, possibleBaseFileName := range possibleBaseFileNames {
		currentPath := strings.Replace(path, "{possibleBaseFileName}", possibleBaseFileName, -1)
		matches, _ := filepath.Glob(os.ExpandEnv(currentPath))
		for _, match := range matches {
			match = getIdeIdePackageByBinary(match)
			foundIdePackages = append(foundIdePackages, match)
		}
	}
	return foundIdePackages, err
}
func getIdeIdePackageByBinary(ideaBinary string) (ideaPackage string) {
	if ideaPackageToWorkWith, err := detectInstallationByInnerPath(ideaBinary, false); err == nil {
		return ideaPackageToWorkWith
	} else {
		log.Printf("Could not get detect ide installation path by binary %s", ideaBinary)
		return ""
	}
}

//If any part of providedPath is IDE installation path, detectInstallationByInnerPath returns path or binary (based on returnBinary flag)
func detectInstallationByInnerPath(providedPath string, returnBinary bool) (ideaBinary string, err error) {
	providedPath = filepath.Clean(providedPath)
	providedDeep := strings.Count(providedPath, string(os.PathSeparator))
	basePath := providedPath
	for i := 1; i < providedDeep; i++ {
		if ideaBinary, err := getIdeBinaryByPackage(basePath); err == nil {
			if returnBinary {
				return ideaBinary, nil
			} else {
				return basePath, nil
			}
		}
		basePath = filepath.Dir(basePath)
	}
	return "", errors.New("Could not detect IDE by \"" + providedPath + "\" path")
}

//getIdeBinaryByPackage return the location of idea(idea.exe) executable inside the IDE installation folder.
//if ideaPackage == /Users/konstantin.annikov/Downloads/IntelliJ IDEA.app
//then idaBinary == /Users/konstantin.annikov/Downloads/IntelliJ IDEA.app/Contents/MacOS/idea
func getIdeBinaryByPackage(ideaPackage string) (ideaBinary string, err error) {
	for _, possibleBaseFileName := range possibleBaseFileNames {
		for operatingSystem, path := range IdeBinaryRelatedToInstallationPath {
			currentBinaryToCheck := strings.Replace(path, "{possibleBaseFileName}", possibleBaseFileName, -1)
			ideaBinary = ideaPackage + currentBinaryToCheck
			if FileExists(ideaBinary) {
				if operatingSystem != runtime.GOOS {
					log.Printf("Provided path is for %s, but repair utility is running at %s ", operatingSystem, runtime.GOOS)
				}
				return filepath.Clean(ideaBinary), nil
			}
		}
	}
	//log.Printf(("Could not detect IDE binary in " + ideaPackage))
	return "", errors.New("Could not detect IDE binary")
}

func getIdeInfoByPackage(ideaPackage string) (parameterValue IdeInfo, err error) {
	var a IdeInfo
	var fileContent []byte
	fileContent, err = ioutil.ReadFile(ideaPackage + IdeProductInfoRelatedToInstallationPath[runtime.GOOS])
	if err != nil {
		for currentOs, path := range IdeProductInfoRelatedToInstallationPath {
			if content, er := ioutil.ReadFile(ideaPackage + path); er == nil {
				fileContent = content
				log.Printf("Could not find product-info.json for %s, but found it for %s ", runtime.GOOS, currentOs)
			}
		}
	}
	err = json.Unmarshal(fileContent, &a)
	return a, err
}

func getIdeLogsDir(ideaBinary string) (logsDir string) {
	if value := GetIdePropertyByName("idea.log.path", ideaBinary); len(value) != 0 {
		if FileExists(value) {
			return value
		} else {
			log.Printf("'idea.log.path' property is defined, but directory \"%s\" does not exist", value)
		}
	}
	installationInfo, err := getIdeInfoByBinary(ideaBinary)
	if err != nil {
		log.Printf("getIdeInfoByBinary failed. ideaBinary: %s, Error: %s", ideaBinary, err)
	}
	logsDir = strings.Replace(defaultLogsDirLocation[runtime.GOOS], "{dataDirectoryName}", installationInfo.DataDirectoryName, -1)
	logsDir = os.ExpandEnv(logsDir)
	if FileExists(logsDir) {
		return logsDir
	} else {
		log.Printf("Could not detect logs directory location for %s. Maybe it has never run?", ideaBinary)
		return ""
	}
}
func GetIdePropertyByName(name string, ideaBinary string) (value string) {
	if len(IdePropertiesMap) == 0 {
		IdePropertiesMap = GetIdeProperties(ideaBinary)
	}
	if _, ok := IdePropertiesMap[name]; ok {
		return IdePropertiesMap[name]
	}
	return ""
}
func getIdeInfoByBinary(ideaBinary string) (parameterValue IdeInfo, err error) {
	return getIdeInfoByPackage(getIdeIdePackageByBinary(ideaBinary))
}
func GetIdeProperties(ideaBinary string) (collectedOptions map[string]string) {
	var ideaPackage string
	collectedOptions = make(map[string]string)
	ideaBinary, _ = DetectInstallationByInnerPath(ideaBinary, true)
	ideaPackage, _ = DetectInstallationByInnerPath(ideaBinary, false)
	InstallationInfo, _ := getIdeInfoByBinary(ideaBinary)
	for _, possibleIdeaPropertiesFileLocation := range getOsDependentDir(possibleIdeaPropertiesFileLocations) {
		possibleIdeaOptionsFile := strings.Replace(possibleIdeaPropertiesFileLocation, "{IDE_BasefileName}", strings.ToUpper(GetIdeBasefileName(ideaBinary)), -1)
		possibleIdeaOptionsFile = strings.Replace(possibleIdeaOptionsFile, "{dataDirectoryName}", InstallationInfo.DataDirectoryName, -1)
		possibleIdeaOptionsFile = strings.Replace(possibleIdeaOptionsFile, "{ideaPackage}", ideaPackage, -1)
		possibleIdeaOptionsFile = os.ExpandEnv(possibleIdeaOptionsFile)
		if FileExists(possibleIdeaOptionsFile) {
			//log.Println("found idea.properties file at: \"" + possibleIdeaOptionsFile + "\"")
			fillIdePropertiesMap(possibleIdeaOptionsFile, collectedOptions)
		} else {
			//log.Println("Checked " + possibleIdeaPropertiesFileLocation + ". There is no \"" + possibleIdeaOptionsFile + "\" file.")
		}
	}
	var listOfCollectedOptions string
	for option, value := range collectedOptions {
		listOfCollectedOptions = listOfCollectedOptions + option + "=" + value + "\n"
	}
	//log.Println("Collected idea properties:\n" + listOfCollectedOptions)
	return collectedOptions
}

//If any part of providedPath is IDE installation path, DetectInstallationByInnerPath returns path or binary (based on returnBinary flag)
func DetectInstallationByInnerPath(providedPath string, returnBinary bool) (ideaBinary string, err error) {
	providedPath = filepath.Clean(providedPath)
	providedDeep := strings.Count(providedPath, string(os.PathSeparator))
	basePath := providedPath
	for i := 1; i < providedDeep; i++ {
		if ideaBinary, err := getIdeBinaryByPackage(basePath); err == nil {
			if returnBinary {
				return ideaBinary, nil
			} else {
				return basePath, nil
			}
		}
		basePath = filepath.Dir(basePath)
	}
	return "", errors.New("Could not detect IDE by \"" + providedPath + "\" path")
}
func fillIdePropertiesMap(ideaOptionsFile string, optionsMap map[string]string) {
	optionsSlice, err := ideaPropertiesFileToSliceOfStrings(ideaOptionsFile)
	if err != nil {
		log.Printf("ideaPropertiesFileToSliceOfStrings failed. ideaOptionsFile: %s, error: %s", ideaOptionsFile, err)
	}

	for _, option := range optionsSlice {
		if idx := strings.IndexByte(option, '='); idx >= 0 {
			optionValue := option[idx+1:]
			optionValue = os.ExpandEnv(optionValue)
			optionName := option[:idx]
			if _, exist := optionsMap[optionName]; !exist {
				optionsMap[optionName] = optionValue
			}
		}

	}
}
func ideaPropertiesFileToSliceOfStrings(ideaPropertiesFile string) (properties []string, err error) {
	file, err := os.Open(ideaPropertiesFile)
	if err != nil {
		log.Printf("failed to open ideaPropertiesFile, file: %s, error: %s", ideaPropertiesFile, err)
	}

	scanner := bufio.NewScanner(file)
	scanner.Split(bufio.ScanLines)
	var i int
	for scanner.Scan() {
		i++
		option := scanner.Text()
		if len(option) != 0 {
			if option[0] == '#' {
			} else {
				properties = append(properties, option)
			}
		}
	}
	err = file.Close()
	if err != nil {
		return nil, err
	}
	return properties, err

}
func GetIdeBasefileName(ideaBinary string) string {
	for _, possibleBaseFileName := range possibleBaseFileNames {
		if strings.HasSuffix(ideaBinary, possibleBaseFileName) {
			return possibleBaseFileName
		}
	}
	return ""
}
func UserHomeDir() string {
	if runtime.GOOS == "windows" {
		home := os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}
		return home
	}
	return os.Getenv("HOME")
}
func FileExists(dir string) bool {
	if f, err := os.Open(dir); err == nil && len(dir) > 2 {
		err := f.Close()
		if err != nil {
			log.Printf("Error closing file %s. Error: %s", dir, err)
		}
		return true
	}
	return false
}
