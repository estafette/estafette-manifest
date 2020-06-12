package manifest

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/xeipuuv/gojsonschema"
	yaml "gopkg.in/yaml.v2"
)

// EstafetteManifest is the object that the .estafette.yaml deserializes to
type EstafetteManifest struct {
	Archived      bool                   `yaml:"archived,omitempty"`
	Builder       EstafetteBuilder       `yaml:"builder,omitempty"`
	Labels        map[string]string      `yaml:"labels,omitempty"`
	Version       EstafetteVersion       `yaml:"version,omitempty"`
	GlobalEnvVars map[string]string      `yaml:"env,omitempty"`
	Metadata      map[string]interface{} `yaml:"metadata,omitempty"`
	Triggers      []*EstafetteTrigger    `yaml:"triggers,omitempty"`
	Stages        []*EstafetteStage      `yaml:"-"`
	Releases      []*EstafetteRelease    `yaml:"-"`
}

// UnmarshalYAML customizes unmarshalling an EstafetteManifest
func (c *EstafetteManifest) UnmarshalYAML(unmarshal func(interface{}) error) (err error) {

	var aux struct {
		Archived            bool                   `yaml:"archived"`
		Builder             EstafetteBuilder       `yaml:"builder"`
		Labels              map[string]string      `yaml:"labels"`
		Version             EstafetteVersion       `yaml:"version"`
		GlobalEnvVars       map[string]string      `yaml:"env"`
		Metadata            map[string]interface{} `yaml:"metadata"`
		DeprecatedPipelines yaml.MapSlice          `yaml:"pipelines"`
		Triggers            []*EstafetteTrigger    `yaml:"triggers"`
		Stages              yaml.MapSlice          `yaml:"stages"`
		Releases            yaml.MapSlice          `yaml:"releases"`
	}

	// unmarshal to auxiliary type
	if err := unmarshal(&aux); err != nil {
		return err
	}

	// map auxiliary properties
	c.Archived = aux.Archived
	c.Builder = aux.Builder
	c.Version = aux.Version
	c.Labels = aux.Labels
	c.GlobalEnvVars = aux.GlobalEnvVars
	c.Metadata = cleanUpStringMap(aux.Metadata)
	c.Triggers = aux.Triggers

	// provide backwards compatibility for the deprecated pipelines section now renamed to stages
	if len(aux.Stages) == 0 && len(aux.DeprecatedPipelines) > 0 {
		aux.Stages = aux.DeprecatedPipelines
	}

	for _, mi := range aux.Stages {

		bytes, err := yaml.Marshal(mi.Value)
		if err != nil {
			return err
		}

		var stage *EstafetteStage
		if err := yaml.Unmarshal(bytes, &stage); err != nil {
			return err
		}
		if stage == nil {
			stage = &EstafetteStage{}
		}

		stage.Name = mi.Key.(string)
		c.Stages = append(c.Stages, stage)
	}

	for _, mi := range aux.Releases {

		bytes, err := yaml.Marshal(mi.Value)
		if err != nil {
			return err
		}

		var release *EstafetteRelease
		if err := yaml.Unmarshal(bytes, &release); err != nil {
			return err
		}
		if release == nil {
			release = &EstafetteRelease{}
		}

		release.Name = mi.Key.(string)
		c.Releases = append(c.Releases, release)
	}

	return nil
}

// MarshalYAML customizes marshalling an EstafetteManifest
func (c EstafetteManifest) MarshalYAML() (out interface{}, err error) {
	var aux struct {
		Archived      bool                   `yaml:"archived,omitempty"`
		Builder       EstafetteBuilder       `yaml:"builder,omitempty"`
		Labels        map[string]string      `yaml:"labels,omitempty"`
		Version       EstafetteVersion       `yaml:"version,omitempty"`
		GlobalEnvVars map[string]string      `yaml:"env,omitempty"`
		Metadata      map[string]interface{} `yaml:"metadata,omitempty"`
		Triggers      []*EstafetteTrigger    `yaml:"triggers,omitempty"`
		Stages        yaml.MapSlice          `yaml:"stages,omitempty"`
		Releases      yaml.MapSlice          `yaml:"releases,omitempty"`
	}

	aux.Archived = c.Archived
	aux.Builder = c.Builder
	aux.Labels = c.Labels
	aux.Version = c.Version
	aux.GlobalEnvVars = c.GlobalEnvVars
	aux.Triggers = c.Triggers
	aux.Metadata = c.Metadata

	for _, stage := range c.Stages {
		aux.Stages = append(aux.Stages, yaml.MapItem{
			Key:   stage.Name,
			Value: stage,
		})
	}
	for _, release := range c.Releases {
		aux.Releases = append(aux.Releases, yaml.MapItem{
			Key:   release.Name,
			Value: release,
		})
	}

	return aux, err
}

// SetDefaults sets default values for properties of EstafetteManifest if not defined
func (c *EstafetteManifest) SetDefaults(preferences EstafetteManifestPreferences) {
	c.Builder.SetDefaults(preferences)
	c.Version.SetDefaults()

	for _, t := range c.Triggers {
		t.SetDefaults("build", "")
	}
	for _, s := range c.Stages {
		s.SetDefaults(c.Builder)
	}

	for _, r := range c.Releases {
		if r.Builder == nil {
			r.Builder = &c.Builder
		} else {
			r.Builder.SetDefaults(preferences)
		}
		for _, t := range r.Triggers {
			t.SetDefaults("release", r.Name)
		}
		for _, s := range r.Stages {
			s.SetDefaults(*r.Builder)
		}
	}
}

// Validate checks if the manifest is valid
func (c *EstafetteManifest) Validate(preferences EstafetteManifestPreferences) (err error) {

	err = c.Builder.validate(preferences)
	if err != nil {
		return
	}

	// loop labels and check if they meet the label regexes
	for key, value := range c.Labels {
		if pattern, ok := preferences.LabelRegexes[key]; ok {
			pattern = fmt.Sprintf("^%v$", strings.TrimSpace(pattern))

			match, err := regexp.MatchString(pattern, value)
			if err != nil {
				return err
			}

			if !match {
				return fmt.Errorf("Label %v does not match regex %v", key, pattern)
			}
		}
	}

	if len(c.Stages) == 0 {
		return fmt.Errorf("The manifest should define 1 or more stages")
	}
	for _, s := range c.Stages {
		err = s.Validate()
		if err != nil {
			return
		}
	}

	for _, t := range c.Triggers {
		err = t.Validate("build", "")
		if err != nil {
			return
		}
	}

	for _, r := range c.Releases {
		if r.Builder != nil {
			err = r.Builder.validate(preferences)
			if err != nil {
				return
			}
		}

		for _, t := range r.Triggers {
			err = t.Validate("release", r.Name)
			if err != nil {
				return
			}
		}

		for _, s := range r.Stages {
			err = s.Validate()
			if err != nil {
				return
			}
		}
	}

	if preferences.MetadataValidationSchema != "" {
		// validate free form metadata section with jsonschema
		metadataBytes, err := json.Marshal(c.Metadata)
		if err != nil {
			return err
		}
		documentLoader := gojsonschema.NewStringLoader(string(metadataBytes))
		schemaLoader := gojsonschema.NewStringLoader(preferences.MetadataValidationSchema)

		result, err := gojsonschema.Validate(schemaLoader, documentLoader)
		if err != nil {
			return err
		}

		if !result.Valid() {
			return fmt.Errorf("Metadata validation failed: %v", result.Errors())
		}
	}

	return nil
}

// GetAllTriggers returns both build and release triggers as one list
func (c *EstafetteManifest) GetAllTriggers(repoSource, repoOwner, repoName string) []EstafetteTrigger {
	// collect both build and release triggers
	triggers := make([]EstafetteTrigger, 0)

	pipelineName := fmt.Sprintf("%v/%v/%v", repoSource, repoOwner, repoName)

	// add all build triggers
	for _, t := range c.Triggers {
		if t != nil {
			t.ReplaceSelf(pipelineName)
			triggers = append(triggers, *t)
		}
	}

	// add all release triggers
	for _, r := range c.Releases {
		for _, t := range r.Triggers {
			if t != nil {
				t.ReplaceSelf(pipelineName)
				triggers = append(triggers, *t)
			}
		}
	}

	return triggers
}

// Exists checks whether the .estafette.yaml exists
func Exists(manifestPath string) bool {

	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		// does not exist
		return false
	}

	// does exist
	return true
}

// ReadManifestFromFile reads the .estafette.yaml into an EstafetteManifest object
func ReadManifestFromFile(preferences *EstafetteManifestPreferences, manifestPath string, validate bool) (manifest EstafetteManifest, err error) {

	log.Debug().Msgf("Reading %v file...", manifestPath)

	// default preferences if not passed
	if preferences == nil {
		preferences = GetDefaultManifestPreferences()
	}

	data, err := ioutil.ReadFile(manifestPath)
	if err != nil {
		return manifest, err
	}

	// unmarshal strict, so non-defined properties or incorrect nesting will fail
	if err := yaml.UnmarshalStrict(data, &manifest); err != nil {
		return manifest, err
	}

	// set defaults
	manifest.SetDefaults(*preferences)

	if validate {
		// check if manifest is valid
		err = manifest.Validate(*preferences)
		if err != nil {
			return manifest, err
		}
	}

	log.Debug().Msgf("Finished reading %v file successfully", manifestPath)

	return
}

// ReadManifest reads the string representation of .estafette.yaml into an EstafetteManifest object
func ReadManifest(preferences *EstafetteManifestPreferences, manifestString string, validate bool) (manifest EstafetteManifest, err error) {

	// default preferences if not passed
	if preferences == nil {
		preferences = GetDefaultManifestPreferences()
	}

	// unmarshal strict, so non-defined properties or incorrect nesting will fail
	if err := yaml.UnmarshalStrict([]byte(manifestString), &manifest); err != nil {
		return manifest, err
	}

	// set defaults
	manifest.SetDefaults(*preferences)

	if validate {
		// check if manifest is valid
		err = manifest.Validate(*preferences)
		if err != nil {
			return manifest, err
		}
	}

	return
}

// GetDefaultManifestPreferences returns default preferences if not configured at the server
func GetDefaultManifestPreferences() *EstafetteManifestPreferences {
	return &EstafetteManifestPreferences{
		LabelRegexes:            map[string]string{},
		BuilderOperatingSystems: []string{"linux", "windows"},
		BuilderTracksPerOperatingSystem: map[string][]string{
			"linux":   {"stable", "beta", "dev"},
			"windows": {"windowsservercore-1809", "windowsservercore-1909", "windowsservercore-ltsc2019"},
		},
	}
}
