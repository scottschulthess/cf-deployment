package standard_test

import (
	"fmt"
	"io/ioutil"
	"og/helpers"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/sergi/go-diff/diffmatchpatch"
	yaml "gopkg.in/yaml.v2"
)

type instanceGroup struct {
	Name      string
	Instances *int
	AZs       []string
	Networks  []struct {
		Name string
	}
	Jobs []struct {
		Properties struct {
			Doppler struct {
				Port *int
			}
		}
	}
}

type releases struct {
	Name string
	URL  string
}

type manifest struct {
	InstanceGroups []instanceGroup `yaml:"instance_groups"`
	Releases       []releases
}

func TestSemantic(t *testing.T) {
	cfDeploymentHome, err := helpers.SetPath()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	operationsSubDirectory := filepath.Join(cfDeploymentHome, "operations")
	manifestPath := filepath.Join(cfDeploymentHome, "cf-deployment.yml")

	t.Run("rename-network-and-deployment.yml", func(t *testing.T) {
		expectedNetworkName := "test_network"

		manifest, err := boshInterpolateAndUnmarshal(
			operationsSubDirectory,
			manifestPath,
			"-o", "rename-network-and-deployment.yml",
			"-v", fmt.Sprintf("network_name=%s", expectedNetworkName),
			"-v", "deployment_name=test_deployment",
		)

		if err != nil {
			t.Errorf("failed to get unmarshalled manifest: %v", err)
		}

		for _, ig := range manifest.InstanceGroups {
			if len(ig.Networks) != 1 {
				t.Errorf("instance group '%s' should only have 1 network", ig.Name)
			}

			networkName := ig.Networks[0].Name
			if networkName != expectedNetworkName {
				t.Errorf("network name '%s' on instance '%s' does not match expected network name '%s'", networkName, ig.Name, expectedNetworkName)
			}
		}
	})

	t.Run("aws.yml", func(t *testing.T) {
		manifest, err := boshInterpolateAndUnmarshal(
			operationsSubDirectory,
			manifestPath,
			"-o", "aws.yml",
		)

		if err != nil {
			t.Errorf("failed to get unmarshalled manifest: %v", err)
		}

		for _, ig := range manifest.InstanceGroups {
			for _, j := range ig.Jobs {
				portNumber := j.Properties.Doppler.Port

				if portNumber != nil && *portNumber != 4443 {
					t.Errorf("port number '%v' on instance '%s' does not match expected port number '%v'", portNumber, ig.Name, 4443)
				}
			}
		}
	})

	t.Run("scale-to-one-az.yml", func(t *testing.T) {
		manifest, err := boshInterpolateAndUnmarshal(
			operationsSubDirectory,
			manifestPath,
			"-o", "scale-to-one-az.yml",
		)

		if err != nil {
			t.Errorf("failed to get unmarshalled manifest: %v", err)
		}

		for _, ig := range manifest.InstanceGroups {
			if ig.Instances != nil && *ig.Instances != 1 {
				t.Errorf("%s has %d instances but expected to have 1", ig.Name, *ig.Instances)
			}
			if len(ig.AZs) != 1 || ig.AZs[0] != "z1" {
				t.Errorf("%s should have single AZ named 'z1'", ig.Name)
			}
		}
	})

	t.Run("use-compiled-releases.yml", func(t *testing.T) {
		manifest, err := boshInterpolateAndUnmarshal(
			operationsSubDirectory,
			manifestPath,
			"-o", "use-compiled-releases.yml",
		)

		if err != nil {
			t.Errorf("failed to get unmarshalled manifest: %v", err)
		}

		for _, r := range manifest.Releases {
			re, err := regexp.Compile(`github\.com|bosh\.com`)
			if err != nil {
				t.Errorf("regexp compile error: %v", err)
				t.Error(err)
			}

			if re.MatchString(r.URL) {
				t.Errorf("expected release %s to be compiled, but got the release from %s", r.Name, r.URL)
			}
		}
	})

	t.Run("use-trusted-ca-cert-for-apps.yml", func(t *testing.T) {
		certPaths := []string{"/instance_groups/name=diego-cell/jobs/name=cflinuxfs2-rootfs-setup/properties/cflinuxfs2-rootfs/trusted_certs",
			"/instance_groups/name=diego-cell/jobs/name=cflinuxfs3-rootfs-setup/properties/cflinuxfs3-rootfs/trusted_certs"}

		for _, certPath := range certPaths {
			existingCA, err := helpers.BoshInterpolate(
				operationsSubDirectory,
				manifestPath,
				"",
				"--path", certPath,
			)
			if err != nil {
				t.Errorf("bosh interpolate error: %v", err)
			}

			newCA, err := helpers.BoshInterpolate(
				operationsSubDirectory,
				manifestPath,
				"",
				"--path", certPath,
				"-o", "use-trusted-ca-cert-for-apps.yml",
			)
			if err != nil {
				t.Errorf("bosh interpolate error: %v", err)
			}

			if diff, ok := diffLeft(string(existingCA), string(newCA)); !ok {
				t.Errorf("use-trusted-ca-cert-for-apps.yml overwrites existing trusted CAs from cf-deployment.yml.\n%s", diff)
			}
		}
	})

	t.Run("add-persistent-isolation-segment-diego-cell.yml", func(t *testing.T) {
		diegoCellRepProperties, err := helpers.BoshInterpolate(
			operationsSubDirectory,
			manifestPath,
			"",
			"--path", "/instance_groups/name=diego-cell/jobs/name=rep/properties",
		)
		if err != nil {
			t.Errorf("bosh interpolate error: %v", err)
		}

		isoSegDiegoCellRepProperties, err := helpers.BoshInterpolate(
			operationsSubDirectory,
			manifestPath,
			"",
			"--path", "/instance_groups/name=isolated-diego-cell/jobs/name=rep/properties",
			"-o", "test/add-persistent-isolation-segment-diego-cell.yml",
		)

		if err != nil {
			t.Errorf("bosh interpolate error: %v", err)
		}

		if diff, ok := diffLeft(string(diegoCellRepProperties), string(isoSegDiegoCellRepProperties)); !ok {
			t.Errorf("rep properties on diego-cell have diverged between cf-deployment.yml and test/add-persistent-isolation-segment-diego-cell.yml.\n%s", diff)
		}
	})

	t.Run("all-cas-referenced-from-ca-variables", func(t *testing.T) {
		caRegexp := regexp.MustCompile(`\(\(.*\.ca\)\)`)
		manifestFile, err := ioutil.ReadFile(manifestPath)
		if err != nil {
			t.Errorf("file read error: %v", err)
		}

		badCAs := caRegexp.FindAllString(string(manifestFile), -1)
		for _, ca := range badCAs {
			t.Errorf("CAs should be referenced from their CA variables: %s", ca)
		}
	})

	t.Run("ops-files-don't-have-double-question-marks", func(t *testing.T) {
		invalid_question_marks := regexp.MustCompile(`path: .*\?.*\?.*`)

		filepath.Walk(operationsSubDirectory, func(path string, info os.FileInfo, err error) error {
			if info.IsDir() {
				return nil
			}

			contents, err := ioutil.ReadFile(path)
			if err != nil {
				t.Errorf("file read error: %v", err)
				return nil
			}

			badPaths := invalid_question_marks.FindAllString(string(contents), -1)
			for _, badPath := range badPaths {
				t.Errorf("%s: Ops files should not contain double '?' in paths: %s", info.Name(), badPath)
			}

			return nil
		})
	})
}

func diffLeft(before, after string) (string, bool) {
	dmp := diffmatchpatch.New()
	beforeDiff, afterDiff, lines := dmp.DiffLinesToChars(before, after)
	diffs := dmp.DiffMain(beforeDiff, afterDiff, true)
	lineDiffs := dmp.DiffCharsToLines(diffs, lines)

	var leftDiffs []diffmatchpatch.Diff
	for _, diff := range diffs {
		if diff.Type == diffmatchpatch.DiffDelete {
			leftDiffs = append(leftDiffs, diff)
		}
	}

	return dmp.DiffPrettyText(lineDiffs), len(leftDiffs) == 0
}

func boshInterpolateAndUnmarshal(opsSubDir, manifestPath string, args ...string) (manifest, error) {
	boshInterpolateOutput, err := helpers.BoshInterpolate(opsSubDir, manifestPath, "", args...)

	if err != nil {
		return manifest{}, fmt.Errorf("bosh interpolate error: %v", err)
	}

	var m manifest
	err = yaml.Unmarshal(boshInterpolateOutput, &m)
	if err != nil {
		return manifest{}, fmt.Errorf("failed to unmarshal bosh interpolate output: %v", err)
	}

	return m, nil
}
