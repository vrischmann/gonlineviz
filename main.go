package main

import (
	"bytes"
	"context"
	"flag"
	"go/build"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/tools/go/vcs"

	"github.com/hirokidaichi/goviz/dotwriter"
	"github.com/hirokidaichi/goviz/goimport"
	"github.com/juju/ratelimit"
	"github.com/pkg/errors"
	"github.com/vrischmann/hutil"
)

func dot(ctx context.Context, r io.Reader, w io.Writer) error {
	var buf bytes.Buffer

	cmd := exec.CommandContext(ctx, "dot", "-Tpng")
	cmd.Stdin = r
	cmd.Stdout = w
	cmd.Stderr = &buf
	cmd.Env = []string{"PATH=/usr/bin:/bin"}

	if err := cmd.Run(); err != nil {
		return errors.Wrapf(err, "output: %s", buf.String())
	}
	return nil
}

var (
	rl = ratelimit.NewBucketWithRate(10, 10)
)

func needsUpdate(packagePath string) bool {
	dir := filepath.Join(gopath, "src", packagePath)

	gitDir := filepath.Join(dir, ".git")

	if _, err := os.Stat(gitDir); err == nil {
		dir = gitDir
	} else {
		// TODO(vincent): implement checking for Mercurial repositories
		return true
	}

	st, err := os.Stat(dir)
	switch {
	case err != nil:
		return true

	case time.Since(st.ModTime()) > 24*time.Hour:
		return true

	default:
		return false
	}
}

func downloadPackage(importPath string) error {
	if importPath == "C" {
		return nil
	}

	vcs.ShowCmd = true

	root, err := vcs.RepoRootForImportPath(importPath, true)
	if err != nil {
		return errors.Wrap(err, "repo root for import path")
	}
	if root != nil && (root.Root == "" || root.Repo == "") {
		return errors.New("empty repo root")
	}

	switch {
	case root.VCS.Name == "Git" && !strings.HasPrefix(importPath, "gopkg.in"):
		root.VCS.CreateCmd = "clone --depth=1 {repo} {dir}"
	default:
		root.VCS.CreateCmd = "clone {repo} {dir}"
	}

	srcPath := filepath.Join(gopath, "src")
	localDirPath := filepath.Join(srcPath, root.Root, "..")
	fullLocalPath := filepath.Join(srcPath, root.Root)

	err = os.MkdirAll(localDirPath, 0700)
	if err != nil {
		return errors.Wrap(err, "mkdir all")
	}

	_, err = os.Stat(fullLocalPath)
	switch {
	case !os.IsNotExist(err) && err != nil:
		return errors.Wrap(err, "stat")

	case os.IsNotExist(err):
		log.Printf("create %s", root.Repo)

		err = root.VCS.Create(fullLocalPath, root.Repo)
		if err != nil {
			return errors.Wrap(err, "vcs create")
		}

	case err == nil:
		log.Printf("update %s", root.Repo)

		err = root.VCS.Download(fullLocalPath)
		if err != nil {
			return errors.Wrap(err, "vcs download")
		}
	}

	err = root.VCS.TagSync(fullLocalPath, "")
	if err != nil {
		return errors.Wrap(err, "vcs tag sync")
	}
	return nil
}

type params struct {
	path     string
	curDepth int
	maxDepth int
	tree     *goimport.ImportPath
}

var errGoroot = errors.New("goroot")

var pkgCache = make(map[string]*build.Package)

func loadPackage(importPath string) (*build.Package, error) {
	p, ok := pkgCache[importPath]
	if ok {
		return p, nil
	}

	log.Printf("not in cache, importing %q", importPath)

	pkg, err := build.Import(importPath, "", 0)
	if err != nil {
		return nil, errors.Wrap(err, "build import")
	}

	pkgCache[importPath] = pkg

	return pkg, nil
}

func downloadAndBuildTree(p params) error {
	if p.curDepth > p.maxDepth || p.path == "C" {
		return nil
	}

	pkg, err := loadPackage(p.path)

	switch {
	case err == nil && pkg.Goroot:
		return errGoroot

	case err != nil:
		if needsUpdate(p.path) {
			if err := downloadPackage(p.path); err != nil {
				return errors.Wrap(err, "download package")
			}
		}

		pkg, err = build.Import(p.path, "", 0)
		if err != nil {
			return errors.Wrap(err, "build import")
		}
	}

	for _, f := range pkg.GoFiles {
		p.tree.Files = append(p.tree.Files, &goimport.Source{
			FileName:  f,
			Namespace: filepath.Base(p.path),
		})
	}

	for _, dep := range pkg.Imports {
		subTree := &goimport.ImportPath{
			ImportPath: dep,
			Files:      nil,
		}

		err := downloadAndBuildTree(params{
			path:     dep,
			curDepth: p.curDepth + 1,
			maxDepth: p.maxDepth,
			tree:     subTree,
		})
		switch {
		case err == errGoroot:
			continue
		case err != nil:
			return err
		default:
			p.tree.AddChild(subTree)
		}
	}

	return nil
}

func getCachedPNG(packagePath string, withLeaf bool) string {
	filename := "dot.png"
	if withLeaf {
		filename = "dot_leaf.png"
	}

	path := filepath.Join(cacheDir, packagePath, filename)

	log.Printf("looking for cached image at %q", path)

	_, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		return ""

	case err != nil:
		log.Printf("unable to stat cache file %q. err=%v", path, err)
		return ""

	default:
		return path
	}
}

func cachePNG(packagePath string, withLeaf bool, r io.Reader) (io.Reader, error) {
	dir := filepath.Join(cacheDir, packagePath)
	err := os.MkdirAll(dir, 0700)
	if err != nil {
		return nil, errors.Wrap(err, "os mkdirall")
	}

	filename := "dot.png"
	if withLeaf {
		filename = "dot_leaf.png"
	}

	path := filepath.Join(dir, filename)

	log.Printf("caching image for %s at %q", packagePath, path)

	f, err := os.Create(path)
	if err != nil {
		return nil, errors.Wrap(err, "os create")
	}

	rd := io.TeeReader(r, f)

	return rd, nil
}

func renderHandler(w http.ResponseWriter, req *http.Request) {
	rl.Wait(1)

	packagePath := req.URL.Path
	if packagePath == "/" || packagePath == "" {
		hutil.WriteBadRequest(w, "please provide a valid package path")
		return
	}

	if packagePath == "/favicon.ico" {
		hutil.WriteText(w, http.StatusNotFound, "Not Found")
		return
	}
	packagePath = packagePath[1:]

	withLeaf := req.URL.Query().Get("leaf") == "true"
	depth := 128
	sDepth := req.URL.Query().Get("depth")
	if sDepth != "" {
		depth, _ = strconv.Atoi(sDepth)
	}
	reversed := req.URL.Query().Get("reversed")

	canUseCache := depth == 128 && reversed == ""

	depsTree := &goimport.ImportPath{
		ImportPath: packagePath,
	}

	err := downloadAndBuildTree(params{
		path:     packagePath,
		curDepth: 0,
		maxDepth: depth,
		tree:     depsTree,
	})
	if err != nil {
		log.Printf("%v", err)
		hutil.WriteText(w, http.StatusInternalServerError, "unable to download package %s", packagePath)
		return
	}

	if canUseCache {
		log.Printf("can use cache for %s", packagePath)
		name := getCachedPNG(packagePath, withLeaf)
		if name != "" {
			http.ServeFile(w, req, name)
			return
		}
	}

	// can't use cache so regen the dot file
	factory := goimport.ParseRelation(packagePath, "", withLeaf)
	if factory == nil {
		err := errors.Errorf("no package %s", packagePath)
		log.Printf("%v", err)
		hutil.WriteError(w, err)
		return
	}

	root := factory.GetRoot()
	if !root.HasFiles() {
		err := errors.Errorf("no .go files in %s", root.ImportPath)
		log.Printf("%v", err)
		hutil.WriteError(w, err)
		return
	}

	var buf bytes.Buffer

	writer := dotwriter.New(&buf)
	writer.MaxDepth = depth

	switch {
	case reversed == "":
		writer.PlotGraph(depsTree)

	default:
		writer.Reversed = true

		// TODO(vincent): gen the reversed tree
	}

	var buf2 bytes.Buffer

	if err := dot(req.Context(), &buf, &buf2); err != nil {
		log.Printf("%s", err)
		hutil.WriteText(w, http.StatusInternalServerError, "unable to call dot")
		return
	}

	// now write the image

	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)

	if canUseCache {
		log.Printf("generated image for %s is cacheable", packagePath)
		// we can cache the image
		rd, err := cachePNG(packagePath, withLeaf, &buf2)
		if err != nil {
			log.Printf("%s", err)
			hutil.WriteText(w, http.StatusInternalServerError, "unable to cache image")
			return
		}

		io.Copy(w, rd)
		return
	}

	io.Copy(w, &buf2)
}

func envVar(key, def string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return def
}

func bEnvVar(key string, def bool) bool {
	if val := os.Getenv(key); val != "" {
		return val == "true" || val == "1"
	}
	return def
}

var (
	flListenAddr = flag.String("l", envVar("LISTEN_ADDR", "localhost:3245"), "The listen address")
	flGoroot     = flag.String("goroot", envVar("GOROOT", "/usr/local/go"), "The GOROOT variable")
	flCGOEnabled = flag.Bool("cgo", bEnvVar("CGO_ENABLED", false), "Enable CGO")
	gopath       = os.Getenv("GOPATH")
	cacheDir     = filepath.Join(os.Getenv("HOME"), ".gonlineviz")
)

func main() {
	flag.Parse()

	if gopath == "" {
		log.Fatal("please set the GOPATH environment variable")
	}

	err := os.Mkdir(cacheDir, 0700)
	if !os.IsExist(err) && err != nil {
		log.Fatal(err)
	}

	var chain hutil.Chain
	chain.Use(hutil.NewLoggingMiddleware(nil))

	mux := http.NewServeMux()
	mux.HandleFunc("/", renderHandler)

	log.Printf("listening on %s", *flListenAddr)
	http.ListenAndServe(*flListenAddr, chain.Handler(mux))
}
