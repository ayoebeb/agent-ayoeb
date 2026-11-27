package previewserver

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/systemexec"
	"github.com/aoagents/agent-orchestrator/backend/internal/agentlaunch"
)

// hostLookup is what building a PATH and resolving an executable need to know
// about this computer. Every field is a seam so tests can fake another one.
type hostLookup struct {
	goos    string
	getenv  func(string) string
	homeDir string
	// persistentPath is the machine and user PATH a new shell would get.
	persistentPath func() string
	isDir          func(string) bool
	isExecutable   func(string) bool
	glob           func(string) []string
	readFile       func(string) string
}

func systemHost() hostLookup {
	home, _ := os.UserHomeDir()
	return hostLookup{
		goos:           runtime.GOOS,
		getenv:         os.Getenv,
		homeDir:        home,
		persistentPath: systemexec.PersistentPath,
		isDir: func(name string) bool {
			info, err := os.Stat(name)
			return err == nil && info.IsDir()
		},
		isExecutable: func(name string) bool {
			info, err := os.Stat(name)
			return err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0o111 != 0)
		},
		glob: func(pattern string) []string {
			matches, _ := filepath.Glob(pattern)
			return matches
		},
		readFile: func(name string) string {
			data, err := os.ReadFile(name) //nolint:gosec // fixed version-manager alias files under the user's home
			if err != nil {
				return ""
			}
			return string(data)
		},
	}
}

func (h hostLookup) windows() bool { return h.goos == "windows" }

func (h hostLookup) listSeparator() string {
	if h.windows() {
		return ";"
	}
	return ":"
}

func (h hostLookup) join(elem ...string) string {
	if h.windows() {
		return strings.ReplaceAll(path.Join(elem...), "/", `\`)
	}
	return path.Join(elem...)
}

// isAbs rejects relative PATH entries such as "." so that nothing is ever
// looked up in whatever directory the server happens to start in.
func (h hostLookup) isAbs(name string) bool {
	if !h.windows() {
		return strings.HasPrefix(name, "/")
	}
	if strings.HasPrefix(name, `\\`) {
		return true
	}
	return len(name) >= 3 && name[1] == ':' && (name[2] == '\\' || name[2] == '/')
}

func (h hostLookup) pathKey(entry string) string {
	if !h.windows() {
		return strings.TrimRight(entry, "/")
	}
	return strings.ToLower(strings.TrimRight(strings.ReplaceAll(entry, "/", `\`), `\`))
}

func (h hostLookup) splitPath(values ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, value := range values {
		for _, entry := range strings.Split(value, h.listSeparator()) {
			entry = strings.TrimSpace(entry)
			if h.windows() {
				entry = strings.Trim(entry, `"`)
			}
			if !h.isAbs(entry) || seen[h.pathKey(entry)] {
				continue
			}
			seen[h.pathKey(entry)] = true
			out = append(out, entry)
		}
	}
	return out
}

// searchPath is the PATH a preview server gets: the daemon's own, then the
// user's persistent one, plus the Node version managers a shell would have set
// up. fnm and nvm go first, as their shell hooks do, unless the PATH already
// has one of their directories.
func (h hostLookup) searchPath(daemonPath string) []string {
	entries := h.splitPath(daemonPath, h.persistentPath())
	contains := func(fragment string) bool {
		for _, entry := range entries {
			if strings.Contains(h.pathKey(entry), h.pathKey(fragment)) {
				return true
			}
		}
		return false
	}
	var first []string
	if fnm := h.fnmBinDir(); fnm != "" && !contains("fnm_multishells") && !contains("node-versions") && !contains(fnm) {
		first = append(first, fnm)
	}
	if nvm := h.nvmBinDir(); nvm != "" && !contains(".nvm") {
		first = append(first, nvm)
	}
	var last []string
	if symlink := h.getenv("NVM_SYMLINK"); h.windows() && symlink != "" && h.isDir(symlink) {
		last = append(last, symlink)
	}
	if volta := h.voltaBinDir(); volta != "" {
		last = append(last, volta)
	}
	sep := h.listSeparator()
	return h.splitPath(strings.Join(first, sep), strings.Join(entries, sep), strings.Join(last, sep))
}

// fnmBinDir is the active multishell directory when the daemon was started from
// an fnm shell, otherwise fnm's default alias.
func (h hostLookup) fnmBinDir() string {
	bin := func(dir string) string {
		if h.windows() {
			return dir
		}
		return h.join(dir, "bin")
	}
	if multishell := h.getenv("FNM_MULTISHELL_PATH"); multishell != "" && h.isDir(bin(multishell)) {
		return bin(multishell)
	}
	var roots []string
	if dir := h.getenv("FNM_DIR"); dir != "" {
		roots = append(roots, dir)
	}
	if xdg := h.getenv("XDG_DATA_HOME"); xdg != "" {
		roots = append(roots, h.join(xdg, "fnm"))
	}
	if appData := h.getenv("APPDATA"); h.windows() && appData != "" {
		roots = append(roots, h.join(appData, "fnm"))
	}
	if h.homeDir != "" {
		switch h.goos {
		case "darwin":
			roots = append(roots, h.join(h.homeDir, "Library", "Application Support", "fnm"))
		case "windows":
		default:
			roots = append(roots, h.join(h.homeDir, ".local", "share", "fnm"))
		}
		roots = append(roots, h.join(h.homeDir, ".fnm"))
	}
	for _, root := range roots {
		if dir := bin(h.join(root, "aliases", "default")); h.isDir(dir) {
			return dir
		}
	}
	return ""
}

// nvmBinDir is nvm's default (or newest) Node on Unix. nvm-windows instead
// persists NVM_SYMLINK, which searchPath handles.
func (h hostLookup) nvmBinDir() string {
	if h.windows() || h.homeDir == "" {
		return ""
	}
	root := h.getenv("NVM_DIR")
	if root == "" {
		root = h.join(h.homeDir, ".nvm")
	}
	alias := ""
	if fields := strings.Fields(h.readFile(h.join(root, "alias", "default"))); len(fields) > 0 {
		alias = fields[0]
	}
	install := agentlaunch.PreferredNodeInstall(h.glob(h.join(root, "versions", "node", "*")), alias)
	if install == "" || !h.isDir(h.join(install, "bin")) {
		return ""
	}
	return h.join(install, "bin")
}

func (h hostLookup) voltaBinDir() string {
	root := h.getenv("VOLTA_HOME")
	if root == "" {
		switch {
		case h.windows() && h.getenv("LOCALAPPDATA") != "":
			root = h.join(h.getenv("LOCALAPPDATA"), "Volta")
		case !h.windows() && h.homeDir != "":
			root = h.join(h.homeDir, ".volta")
		default:
			return ""
		}
	}
	if dir := h.join(root, "bin"); h.isDir(dir) {
		return dir
	}
	return ""
}

// withSearchPath gives env exactly one PATH and returns the directories a bare
// executable name is looked up in. A PATH set by the project or the launch
// config is the user's choice and is passed through as written.
func (h hostLookup) withSearchPath(env []string, configured ...map[string]string) ([]string, []string) {
	isPathKey := func(key string) bool {
		return key == "PATH" || h.windows() && strings.EqualFold(key, "PATH")
	}
	explicit := false
	for _, values := range configured {
		for key := range values {
			explicit = explicit || isPathKey(key)
		}
	}
	value := ""
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if key, current, ok := strings.Cut(entry, "="); ok && isPathKey(key) {
			value = current
			continue
		}
		out = append(out, entry)
	}
	var searchPath []string
	if explicit {
		searchPath = h.splitPath(value)
	} else {
		searchPath = h.searchPath(value)
		value = strings.Join(searchPath, h.listSeparator())
	}
	return append(out, "PATH="+value), searchPath
}

// executableCandidates are the file names that can satisfy name. Windows only
// runs a file by its extension, so a bare "npm" means npm.cmd or npm.exe, never
// the extensionless shell script npm ships next to them.
func (h hostLookup) executableCandidates(name, pathext string) []string {
	if !h.windows() {
		return []string{name}
	}
	if pathext == "" {
		pathext = ".COM;.EXE;.BAT;.CMD"
	}
	var extensions []string
	for _, ext := range strings.Split(pathext, ";") {
		switch ext = strings.ToLower(strings.TrimSpace(ext)); ext {
		case ".com", ".exe", ".bat", ".cmd":
			extensions = append(extensions, ext)
		}
	}
	var out []string
	for _, ext := range extensions {
		if strings.HasSuffix(strings.ToLower(name), ext) {
			out = append(out, name)
			break
		}
	}
	for _, ext := range extensions {
		out = append(out, name+ext)
	}
	return out
}

// resolveExecutable turns name into an absolute file. A name with a path
// separator is relative to workingDir; a bare name is searched on searchPath
// only, never in workingDir.
func (h hostLookup) resolveExecutable(name, workingDir string, searchPath []string, pathext string) (string, error) {
	separators := "/"
	if h.windows() {
		separators = `/\`
	}
	if strings.ContainsAny(name, separators) {
		target := name
		if !h.isAbs(target) {
			target = h.join(workingDir, target)
		}
		for _, candidate := range h.executableCandidates(target, pathext) {
			if h.isExecutable(candidate) {
				return candidate, nil
			}
		}
		return "", serviceError("PREVIEW_EXECUTABLE_NOT_FOUND",
			"preview executable \""+name+"\" was not found at "+target)
	}
	for _, dir := range searchPath {
		for _, candidate := range h.executableCandidates(name, pathext) {
			if full := h.join(dir, candidate); h.isExecutable(full) {
				return full, nil
			}
		}
	}
	return "", serviceError("PREVIEW_EXECUTABLE_NOT_FOUND",
		"preview executable \""+name+"\" was not found. PATH searched: "+strings.Join(searchPath, h.listSeparator()))
}
