package workers

import (
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/nyancrimew/goop/internal/utils"
	"github.com/nyancrimew/jobtracker"
	"github.com/phuslu/log"
	"github.com/valyala/fasthttp"
	"gopkg.in/ini.v1"
)

var refRegex = regexp.MustCompile(`(?m)(refs(/[a-zA-Z0-9\-\.\_\*]+)+)`)
var branchRegex = regexp.MustCompile(`(?m)branch ["'](.+)["']`)

var checkedRefs = make(map[string]bool)
var checkedRefsMutex sync.Mutex

type FindRefContext struct {
	C       *fasthttp.Client
	BaseUrl string
	BaseDir string
}

func FindRefWorker(jt *jobtracker.JobTracker, path string, context jobtracker.Context) {
	c := context.(FindRefContext)

	checkRatelimted()

	checkedRefsMutex.Lock()
	if checked, ok := checkedRefs[path]; checked && ok {
		// Ref has already been checked
		checkedRefsMutex.Unlock()
		return
	} else {
		checkedRefs[path] = true
	}
	checkedRefsMutex.Unlock()

	targetFile := utils.Url(c.BaseDir, path)
	if path == ".git/config" || path == ".git/config.worktree" {
		targetFile = utils.Url(c.BaseDir, path+".goop")
	}
	if utils.Exists(targetFile) {
		log.Info().Str("file", targetFile).Msg("already fetched, skipping redownload")
		content, err := ioutil.ReadFile(targetFile)
		if err != nil {
			log.Error().Str("file", targetFile).Err(err).Msg("error while reading file")
			return
		}
		for _, ref := range refRegex.FindAll(content, -1) {
			jt.AddJob(utils.Url(".git", string(ref)))
			jt.AddJob(utils.Url(".git/logs", string(ref)))
		}
		if path == ".git/FETCH_HEAD" {
			// TODO figure out actual remote instead of just assuming origin here (if possible)
			for _, branch := range branchRegex.FindAllSubmatch(content, -1) {
				jt.AddJob(fmt.Sprintf(".git/refs/remotes/origin/%s", branch[1]))
				jt.AddJob(fmt.Sprintf(".git/logs/refs/remotes/origin/%s", branch[1]))
			}
		}
		if path == ".git/config" || path == ".git/config.worktree" {
			cfg, err := ini.Load(content)
			if err != nil {
				log.Error().Str("file", targetFile).Err(err).Msg("failed to parse git config")
				return
			}
			for _, sec := range cfg.Sections() {
				if strings.HasPrefix(sec.Name(), "branch ") {
					parts := strings.SplitN(sec.Name(), " ", 2)
					branch := strings.Trim(parts[1], `"`)
					remote := sec.Key("remote").String()

					jt.AddJob(fmt.Sprintf(".git/refs/remotes/%s/%s", remote, branch))
					jt.AddJob(fmt.Sprintf(".git/logs/refs/remotes/%s/%s", remote, branch))
				}
			}
		}
		return
	}

	uri := utils.Url(c.BaseUrl, path)
	code, body, err := c.C.Get(nil, uri)
	if err == nil && code != 200 {
		if code == 429 {
			setRatelimited()
			jt.AddJob(path)
			return
		}
		log.Warn().Str("uri", uri).Int("code", code).Msg("failed to fetch ref")
		return
	} else if err != nil {
		log.Error().Str("uri", uri).Int("code", code).Err(err).Msg("failed to fetch ref")
		return
	}

	if utils.IsHtml(body) {
		log.Warn().Str("uri", uri).Msg("file appears to be html, skipping")
		return
	}
	if utils.IsEmptyBytes(body) {
		log.Warn().Str("uri", uri).Msg("file appears to be empty, skipping")
		return
	}
	if err := utils.CreateParentFolders(targetFile); err != nil {
		log.Error().Str("uri", uri).Str("file", targetFile).Err(err).Msg("couldn't create parent directories")
		return
	}
	if err := ioutil.WriteFile(targetFile, body, os.ModePerm); err != nil {
		log.Error().Str("uri", uri).Str("file", targetFile).Err(err).Msg("clouldn't write file")
		return
	}

	log.Info().Str("uri", uri).Msg("fetched ref")

	for _, ref := range refRegex.FindAll(body, -1) {
		jt.AddJob(utils.Url(".git", string(ref)))
		jt.AddJob(utils.Url(".git/logs", string(ref)))
	}
	if path == ".git/FETCH_HEAD" {
		// TODO figure out actual remote instead of just assuming origin here (if possible)
		for _, branch := range branchRegex.FindAllSubmatch(body, -1) {
			jt.AddJob(fmt.Sprintf(".git/refs/remotes/origin/%s", branch[1]))
			jt.AddJob(fmt.Sprintf(".git/logs/refs/remotes/origin/%s", branch[1]))
		}
	}
	if path == ".git/config" || path == ".git/config.worktree" {
		cfg, err := ini.Load(body)
		if err != nil {
			log.Error().Str("file", targetFile).Err(err).Msg("failed to parse git config")
			return
		}
		for _, sec := range cfg.Sections() {
			if strings.HasPrefix(sec.Name(), "branch ") {
				parts := strings.SplitN(sec.Name(), " ", 2)
				branch := strings.Trim(parts[1], `"`)
				remote := sec.Key("remote").String()

				jt.AddJob(fmt.Sprintf(".git/refs/remotes/%s/%s", remote, branch))
				jt.AddJob(fmt.Sprintf(".git/logs/refs/remotes/%s/%s", remote, branch))
			}
		}
	}
}
