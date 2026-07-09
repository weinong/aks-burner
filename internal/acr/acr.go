package acr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxTagLength = 128

var imageKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

type RegistryConfig struct {
	NameParameter string `yaml:"nameParameter"`
}

type Requirements struct {
	Registry RegistryConfig `yaml:"registry"`
	Builds   []ImageBuild   `yaml:"builds"`
}

type ImageBuild struct {
	Key            string            `yaml:"key"`
	Repository     string            `yaml:"repository"`
	Context        string            `yaml:"context"`
	Dockerfile     string            `yaml:"dockerfile"`
	Platform       string            `yaml:"platform"`
	TimeoutSeconds int               `yaml:"timeoutSeconds"`
	BuildArgs      map[string]string `yaml:"buildArgs"`
}

type BuiltImage struct {
	Key        string `yaml:"key"`
	Image      string `yaml:"image"`
	Repository string `yaml:"repository"`
	Tag        string `yaml:"tag"`
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile"`
}

type BuildOptions struct {
	SuiteDir       string
	RegistryName   string
	RegistryServer string
	ResourceGroup  string
	Tag            string
	Builds         []ImageBuild
	LogsDir        string
}

func RunTag(suiteName string, mode string, timestamp time.Time) string {
	value := fmt.Sprintf("%s-%s-%s", suiteName, mode, timestamp.UTC().Format("20060102T150405.000000000Z"))
	return sanitizeTag(value)
}

func BuildCommands(opts BuildOptions) ([][]string, []BuiltImage, error) {
	if err := validateOptions(opts); err != nil {
		return nil, nil, err
	}
	if len(opts.Tag) > maxTagLength {
		return nil, nil, fmt.Errorf("image tag length %d exceeds maximum %d", len(opts.Tag), maxTagLength)
	}
	commands := make([][]string, 0, len(opts.Builds))
	built := make([]BuiltImage, 0, len(opts.Builds))
	seenKeys := map[string]bool{}
	for _, build := range opts.Builds {
		if err := validateBuild(build); err != nil {
			return nil, nil, err
		}
		if seenKeys[build.Key] {
			return nil, nil, fmt.Errorf("duplicate image build key %q", build.Key)
		}
		seenKeys[build.Key] = true
		contextPath, err := resolveContextPath(opts.SuiteDir, build.Context)
		if err != nil {
			return nil, nil, err
		}
		dockerfile := build.Dockerfile
		if dockerfile == "" {
			dockerfile = "Dockerfile"
		}
		if !safeRelativePath(dockerfile) {
			return nil, nil, fmt.Errorf("image build %q dockerfile must be relative to context", build.Key)
		}
		if err := validateDockerfilePath(contextPath, dockerfile, build.Key); err != nil {
			return nil, nil, err
		}
		imageName := build.Repository + ":" + opts.Tag
		args := []string{"az", "acr", "build", "--registry", opts.RegistryName}
		if opts.ResourceGroup != "" {
			args = append(args, "--resource-group", opts.ResourceGroup)
		}
		args = append(args, "--image", imageName, "--file", dockerfile)
		if build.Platform != "" {
			args = append(args, "--platform", build.Platform)
		}
		if build.TimeoutSeconds > 0 {
			args = append(args, "--timeout", strconv.Itoa(build.TimeoutSeconds))
		}
		for _, key := range sortedKeys(build.BuildArgs) {
			args = append(args, "--build-arg", key+"="+build.BuildArgs[key])
		}
		args = append(args, contextPath)
		commands = append(commands, args)
		built = append(built, BuiltImage{
			Key:        build.Key,
			Image:      opts.RegistryServer + "/" + imageName,
			Repository: build.Repository,
			Tag:        opts.Tag,
			Context:    build.Context,
			Dockerfile: dockerfile,
		})
	}
	return commands, built, nil
}

func Build(ctx context.Context, opts BuildOptions) ([]BuiltImage, map[string]string, error) {
	commands, built, err := BuildCommands(opts)
	if err != nil {
		return nil, nil, err
	}
	for i, args := range commands {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		var logFile *os.File
		if opts.LogsDir != "" {
			if err := os.MkdirAll(opts.LogsDir, 0o755); err != nil {
				return nil, nil, err
			}
			path := filepath.Join(opts.LogsDir, "acr-build-"+safeFileName(built[i].Key)+".log")
			logFile, err = os.Create(path)
			if err != nil {
				return nil, nil, err
			}
			cmd.Stdout = logFile
			cmd.Stderr = logFile
		} else {
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
		}
		if err := cmd.Run(); err != nil {
			if logFile != nil {
				_ = logFile.Close()
			}
			return nil, nil, err
		}
		if logFile != nil {
			if err := logFile.Close(); err != nil {
				return nil, nil, err
			}
		}
	}
	images := map[string]string{}
	for _, image := range built {
		images[image.Key] = image.Image
	}
	return built, images, nil
}

func ValidateBuilds(builds []ImageBuild) error {
	for _, build := range builds {
		if err := validateBuild(build); err != nil {
			return err
		}
	}
	return nil
}

func validateOptions(opts BuildOptions) error {
	if opts.SuiteDir == "" {
		return fmt.Errorf("suite directory is required")
	}
	if opts.RegistryName == "" {
		return fmt.Errorf("registry name is required")
	}
	if opts.RegistryServer == "" {
		return fmt.Errorf("registry server is required")
	}
	if opts.Tag == "" {
		return fmt.Errorf("image tag is required")
	}
	return nil
}

func validateBuild(build ImageBuild) error {
	if build.Key == "" {
		return fmt.Errorf("image build key is required")
	}
	if !imageKeyPattern.MatchString(build.Key) {
		return fmt.Errorf("image build key %q must contain only letters, digits, dots, underscores, or hyphens", build.Key)
	}
	if build.Repository == "" {
		return fmt.Errorf("image build %q repository is required", build.Key)
	}
	if strings.Contains(build.Repository, ":") || strings.Contains(build.Repository, ".azurecr.io/") || strings.HasPrefix(build.Repository, "/") {
		return fmt.Errorf("image build %q repository must not include registry host or tag", build.Key)
	}
	if build.Context == "" {
		return fmt.Errorf("image build %q context is required", build.Key)
	}
	return nil
}

func resolveContextPath(suiteDir string, value string) (string, error) {
	if !safeRelativePath(value) && filepath.Clean(value) != "." {
		return "", fmt.Errorf("image build context %q resolves outside suite directory %q", value, suiteDir)
	}
	cleanValue := filepath.Clean(value)
	resolved := filepath.Join(suiteDir, cleanValue)
	resolved = filepath.Clean(resolved)
	if !pathInsideOrEqual(suiteDir, resolved) {
		return "", fmt.Errorf("image build context %q resolves outside suite directory %q", value, suiteDir)
	}
	realSuiteDir, err := filepath.EvalSymlinks(suiteDir)
	if err != nil {
		return "", err
	}
	realResolved, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	if !pathInsideOrEqual(realSuiteDir, realResolved) {
		return "", fmt.Errorf("image build context %q resolves outside suite directory %q", value, suiteDir)
	}
	return resolved, nil
}

func validateDockerfilePath(contextPath string, dockerfile string, key string) error {
	dockerfilePath := filepath.Join(contextPath, dockerfile)
	info, err := os.Lstat(dockerfilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("image build %q dockerfile must not be a symlink", key)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("image build %q dockerfile must be a regular file", key)
	}
	realContextPath, err := filepath.EvalSymlinks(contextPath)
	if err != nil {
		return err
	}
	realDockerfilePath, err := filepath.EvalSymlinks(dockerfilePath)
	if err != nil {
		return err
	}
	if !pathInsideOrEqual(realContextPath, realDockerfilePath) {
		return fmt.Errorf("image build %q dockerfile resolves outside context", key)
	}
	return nil
}

func safeRelativePath(value string) bool {
	cleanValue := filepath.Clean(value)
	return cleanValue != ".." && !strings.HasPrefix(cleanValue, ".."+string(filepath.Separator)) && !filepath.IsAbs(cleanValue)
}

func pathInsideOrEqual(base string, target string) bool {
	rel, err := filepath.Rel(base, target)
	return err == nil && (rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)))
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sanitizeTag(value string) string {
	re := regexp.MustCompile(`[^A-Za-z0-9_.-]`)
	return re.ReplaceAllString(value, "-")
}

func safeFileName(value string) string {
	name := sanitizeTag(value)
	if name == "" {
		return "image"
	}
	return name
}
