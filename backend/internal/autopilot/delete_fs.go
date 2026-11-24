package autopilot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// maxDeleteTreeEntries bounds how many entries of a directory tree are looked at
// before a delete is parked: a tree that big is not one AO can vouch for.
var maxDeleteTreeEntries = 20000

// A delete whose text names a path inside the workspace can still reach outside
// it: a symlink, a Windows junction or any other reparse point inside the
// workspace may point anywhere, on the target itself, on one of its parents or on
// something deep in the tree a recursive delete walks. So before a canonical
// delete is allowed, the real paths are resolved on the filesystem
// (checkDeleteTargets). Anything that cannot be resolved or inspected is parked.
//
// This checks the state of the disk at the moment of the request. A link made
// between the check and the delete is out of its reach; the agent that could make
// one could also run the delete itself, which is why the check sits next to the
// textual rules and does not replace them.

// checkDeleteTargets reports whether every target (absolute, already cleaned
// lexically) really lies strictly inside the workspace. It returns the reason when
// one does not or cannot be shown to.
func checkDeleteTargets(workspace string, targets []string) (bool, string) {
	realRoot, err := filepath.EvalSymlinks(filepath.FromSlash(workspace))
	if err != nil {
		return false, "AO cannot resolve the workspace on disk, so it cannot show the delete stays inside it."
	}
	realRoot = filepath.Clean(realRoot)
	for _, target := range targets {
		if ok, reason := checkDeleteTarget(realRoot, filepath.FromSlash(target)); !ok {
			return false, reason
		}
	}
	return true, ""
}

func checkDeleteTarget(realRoot, target string) (bool, string) {
	info, err := os.Lstat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Nothing to delete, but the directory it would sit in must still be real
		// and inside: the deepest ancestor that exists is resolved.
		return checkAncestor(realRoot, target)
	case err != nil:
		return false, fmt.Sprintf("AO cannot inspect %s (%v), so it cannot show the delete stays inside the workspace.", target, err)
	}
	if isLink(info) {
		return false, fmt.Sprintf("%s is a symbolic link, junction or reparse point, which may lead outside the workspace.", target)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false, fmt.Sprintf("AO cannot resolve %s on disk (%v), so it cannot show the delete stays inside the workspace.", target, err)
	}
	if !strictlyInside(realRoot, resolved) {
		return false, fmt.Sprintf("%s resolves to %s, outside the workspace (a parent is a link).", target, resolved)
	}
	if info.IsDir() {
		return checkTree(target)
	}
	return true, ""
}

// checkAncestor resolves the deepest existing ancestor of a path that does not
// exist, and requires it to be inside the workspace (or the workspace itself).
func checkAncestor(realRoot, target string) (bool, string) {
	dir := filepath.Dir(target)
	for {
		info, err := os.Lstat(dir)
		if err == nil {
			if isLink(info) {
				return false, fmt.Sprintf("%s is a symbolic link, junction or reparse point, which may lead outside the workspace.", dir)
			}
			resolved, err := filepath.EvalSymlinks(dir)
			if err != nil {
				return false, fmt.Sprintf("AO cannot resolve %s on disk (%v).", dir, err)
			}
			if resolved != realRoot && !strictlyInside(realRoot, resolved) {
				return false, fmt.Sprintf("%s resolves to %s, outside the workspace.", dir, resolved)
			}
			return true, ""
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Sprintf("AO cannot inspect %s (%v).", dir, err)
		}
		next := filepath.Dir(dir)
		if next == dir {
			return false, "AO found no existing parent directory for the delete target."
		}
		dir = next
	}
}

// checkTree walks a directory a delete would remove, without following links. Any
// link, junction or reparse point in it, an entry that cannot be read, or more than
// maxDeleteTreeEntries entries parks the delete.
func checkTree(root string) (bool, string) {
	count := 0
	var reason string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			reason = fmt.Sprintf("AO cannot read %s (%v), so it cannot show the delete stays inside the workspace.", p, walkErr)
			return walkErr
		}
		count++
		if count > maxDeleteTreeEntries {
			reason = fmt.Sprintf("%s holds more than %d entries, too many for AO to check for links that lead outside the workspace.", root, maxDeleteTreeEntries)
			return errTreeTooBig
		}
		if d.Type()&fs.ModeSymlink != 0 {
			reason = fmt.Sprintf("%s is a symbolic link inside the tree to delete; it may lead outside the workspace.", p)
			return errTreeLink
		}
		info, err := d.Info()
		if err != nil {
			reason = fmt.Sprintf("AO cannot inspect %s (%v).", p, err)
			return err
		}
		if isLink(info) {
			reason = fmt.Sprintf("%s is a junction or reparse point inside the tree to delete; it may lead outside the workspace.", p)
			return errTreeLink
		}
		return nil
	})
	if err != nil {
		if reason == "" {
			reason = fmt.Sprintf("AO could not walk %s (%v).", root, err)
		}
		return false, reason
	}
	return true, ""
}

var (
	errTreeTooBig = errors.New("tree too big")
	errTreeLink   = errors.New("link in tree")
)

// isLink reports a symbolic link, or on Windows a junction or any other reparse
// point (see reparse_windows.go).
func isLink(info fs.FileInfo) bool {
	return info.Mode()&fs.ModeSymlink != 0 || info.Mode()&fs.ModeIrregular != 0 || isReparsePoint(info)
}

// strictlyInside reports whether path lies below root (and is not root itself),
// comparing real paths; Windows paths ignore case.
func strictlyInside(root, path string) bool {
	root, path = filepath.Clean(root), filepath.Clean(path)
	if filepath.VolumeName(root) != "" || filepath.VolumeName(path) != "" {
		root, path = strings.ToLower(root), strings.ToLower(path)
	}
	if path == root {
		return false
	}
	return strings.HasPrefix(path, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}
