// Copyright 2016-2021, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// This file implements the methods used by the Coverage Tracker in order
// to export the data it collected into various JSON formats.

package tfgen

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
)

// The export utility's main structure, where it stores the desired output directory
// and a reference to the CoverageTracker that created it
type coverageExportUtil struct {
	Tracker *CoverageTracker // Reference to the Coverage Tracker that wants to turn its data into a file
}

func newCoverageExportUtil(coverageTracker *CoverageTracker) coverageExportUtil {
	return coverageExportUtil{coverageTracker}
}

// The entire export utility interface. Will attempt to export the Coverage Tracker's data into the
// specified output directory, and will panic if an error is encountered along the way
func (ce *coverageExportUtil) tryExport(outputDirectory string) error {

	// "summary.json" is the file name that other Pulumi coverage trackers use
	var err = ce.exportByExample(outputDirectory, "byExample.json")
	if err != nil {
		return err
	}
	err = ce.exportByLanguage(outputDirectory, "byLanguage.json")
	if err != nil {
		return err
	}

	// `summary.json` & `shortSummary.txt` are magic filenames used by pulumi/ci-mgmt/provider-ci.
	// If it finds these files, `summary.json` gets uploaded to S3 for cloudwatch analysis, and
	// `shortSummary.txt` is read by the terminal to be visible in Github Actions for inspection
	err = ce.exportOverall(outputDirectory, "summary.json")
	if err != nil {
		return err
	}
	return ce.exportHumanReadable(outputDirectory, "shortSummary.txt")
}

// Four different ways to export coverage data:
// The first mode, which lists each example individually in one big file. This is the most detailed.
func (ce *coverageExportUtil) exportByExample(outputDirectory string, fileName string) error {

	// The Coverage Tracker data structure is flattened down to the example level, and they all
	// get individually written to the file in order to not have the "{ }" brackets at the start and end
	type SingleExampleResult struct {
		ProviderName    string
		ProviderVersion string
		ExampleName     string
		OriginalHCL     string `json:"OriginalHCL,omitempty"`
		IsDuplicated    bool
		FailedLanguages []LanguageConversionResult `json:"FailedLanguages,omitempty"`
	}

	jsonOutputLocation, err := createEmptyFile(outputDirectory, fileName)
	if err != nil {
		return err
	}

	// All the examples in the map are iterated by key and marshalled into one large byte array
	// separated by \n, making the end result look like a bunch of Json files that got concatenated
	var result []byte
	for _, exampleInMap := range ce.Tracker.EncounteredExamples {
		singleExample := SingleExampleResult{
			ProviderName:    ce.Tracker.ProviderName,
			ProviderVersion: ce.Tracker.ProviderVersion,
			ExampleName:     exampleInMap.Name,
			OriginalHCL:     "",
			FailedLanguages: []LanguageConversionResult{},
		}

		// The current example's language conversion results are iterated over. If the severity is
		// anything but zero, then it means some sort of error occurred during conversion and
		// should be logged for future analysis.
		for _, conversionResult := range exampleInMap.LanguagesConvertedTo {
			if conversionResult.FailureSeverity != 0 {
				singleExample.OriginalHCL = exampleInMap.OriginalHCL
				singleExample.FailedLanguages = append(singleExample.FailedLanguages, *conversionResult)
			}
			singleExample.IsDuplicated = singleExample.IsDuplicated || conversionResult.MultipleTranslations
		}
		marshalledExample, err := json.MarshalIndent(singleExample, "", "\t")
		if err != nil {
			return err
		}
		result = append(append(result, marshalledExample...), uint8('\n'))
	}
	return ioutil.WriteFile(jsonOutputLocation, result, 0600)
}

// The second mode, which exports information about each language such as total number of
// examples, common failure messages, and failure severity percentages.
func (ce *coverageExportUtil) exportByLanguage(outputDirectory string, fileName string) error {

	// The Coverage Tracker data structure is flattened to gather statistics about each language
	type NumPct struct {
		Number int
		Pct    float64
	}

	type ErrorMessage struct {
		Reason string
		Count  int
	}

	type LanguageStatistic struct {
		Total           int
		Successes       NumPct
		Warnings        NumPct
		Failures        NumPct
		Fatals          NumPct
		_errorHistogram map[string]int
		FrequentErrors  []ErrorMessage
	}

	// Main map for holding all the language conversion statistics
	var allLanguageStatistics = make(map[string]*LanguageStatistic)

	// All the conversion attempts for each example are iterated by language name and
	// their results are added to the main map
	for _, exampleInMap := range ce.Tracker.EncounteredExamples {
		for _, conversionResult := range exampleInMap.LanguagesConvertedTo {
			var language *LanguageStatistic
			if val, ok := allLanguageStatistics[conversionResult.TargetLanguage]; ok {

				// The main map already contains the language entry
				language = val
			} else {

				// The main map doesn't yet contain this language, and it needs to be added
				allLanguageStatistics[conversionResult.TargetLanguage] = &LanguageStatistic{0,
					NumPct{0, 0.0}, NumPct{0, 0.0},
					NumPct{0, 0.0}, NumPct{0, 0.0},
					make(map[string]int), []ErrorMessage{}}
				language = allLanguageStatistics[conversionResult.TargetLanguage]
			}

			// The language's entry in the summarized results is updated and any
			// error messages are saved
			language.Total++
			if conversionResult.FailureSeverity == Success {
				language.Successes.Number++
			} else {

				// A failure occurred during conversion so we take the failure info
				// and add it to the histogram
				language._errorHistogram[conversionResult.FailureInfo]++

				switch conversionResult.FailureSeverity {
				case Warning:
					language.Warnings.Number++
				case Failure:
					language.Failures.Number++
				default:
					language.Fatals.Number++
				}
			}
		}
	}

	for _, language := range allLanguageStatistics {

		// Calculating error percentages for all languages that were found
		language.Successes.Pct = float64(language.Successes.Number) / float64(language.Total) * 100.0
		language.Warnings.Pct = float64(language.Warnings.Number) / float64(language.Total) * 100.0
		language.Failures.Pct = float64(language.Failures.Number) / float64(language.Total) * 100.0
		language.Fatals.Pct = float64(language.Fatals.Number) / float64(language.Total) * 100.0

		// Appending and sorting conversion errors by their frequency
		for reason, count := range language._errorHistogram {
			language.FrequentErrors = append(language.FrequentErrors, ErrorMessage{reason, count})
		}
		sort.Slice(language.FrequentErrors, func(index1, index2 int) bool {
			if language.FrequentErrors[index1].Count != language.FrequentErrors[index2].Count {
				return language.FrequentErrors[index1].Count > language.FrequentErrors[index2].Count
			}

			return language.FrequentErrors[index1].Reason > language.FrequentErrors[index2].Reason
		})
	}

	jsonOutputLocation, err := createEmptyFile(outputDirectory, fileName)
	if err != nil {
		return err
	}
	return marshalAndWriteJSON(allLanguageStatistics, jsonOutputLocation)
}

// The third mode, which lists failure reaons, quantities and percentages for the provider as a whole.
func (ce *coverageExportUtil) exportOverall(outputDirectory string, fileName string) error {

	// The Coverage Tracker data structure is flattened to gather statistics about the provider
	type NumPct struct {
		Number int
		Pct    float64
	}

	type ErrorMessage struct {
		Reason string
		Count  int
	}

	type ProviderStatistic struct {
		Name             string
		Version          string
		Examples         int
		TotalConversions int
		Successes        NumPct
		Warnings         NumPct
		Failures         NumPct
		Fatals           NumPct
		_errorHistogram  map[string]int
		ConversionErrors []ErrorMessage
	}

	// Main variable for holding the overall provider conversion results
	var providerStatistic = ProviderStatistic{ce.Tracker.ProviderName,
		ce.Tracker.ProviderVersion, 0, 0, NumPct{0, 0.0},
		NumPct{0, 0.0}, NumPct{0, 0.0},
		NumPct{0, 0.0}, make(map[string]int), []ErrorMessage{}}

	// All the conversion attempts for each example are iterated by language name and
	// their results are added to the overall statistic
	for _, exampleInMap := range ce.Tracker.EncounteredExamples {
		providerStatistic.Examples++
		for _, conversionResult := range exampleInMap.LanguagesConvertedTo {
			providerStatistic.TotalConversions++
			if conversionResult.FailureSeverity == Success {
				providerStatistic.Successes.Number++
			} else {

				// A failure occurred during conversion so we take the failure info
				// and add it to the histogram
				providerStatistic._errorHistogram[conversionResult.FailureInfo]++

				switch conversionResult.FailureSeverity {
				case Warning:
					providerStatistic.Warnings.Number++
				case Failure:
					providerStatistic.Failures.Number++
				default:
					providerStatistic.Fatals.Number++
				}
			}
		}
	}

	// Calculating overall error percentages
	providerStatistic.Successes.Pct = float64(providerStatistic.Successes.Number) /
		float64(providerStatistic.TotalConversions) * 100.0
	providerStatistic.Warnings.Pct = float64(providerStatistic.Warnings.Number) /
		float64(providerStatistic.TotalConversions) * 100.0
	providerStatistic.Failures.Pct = float64(providerStatistic.Failures.Number) /
		float64(providerStatistic.TotalConversions) * 100.0
	providerStatistic.Fatals.Pct = float64(providerStatistic.Fatals.Number) /
		float64(providerStatistic.TotalConversions) * 100.0

	// Appending and sorting conversion errors by their frequency
	for reason, count := range providerStatistic._errorHistogram {
		providerStatistic.ConversionErrors = append(providerStatistic.ConversionErrors,
			ErrorMessage{reason, count})
	}
	sort.Slice(providerStatistic.ConversionErrors, func(index1, index2 int) bool {
		if providerStatistic.ConversionErrors[index1].Count != providerStatistic.ConversionErrors[index2].Count {
			return providerStatistic.ConversionErrors[index1].Count > providerStatistic.ConversionErrors[index2].Count
		}

		return providerStatistic.ConversionErrors[index1].Reason > providerStatistic.ConversionErrors[index2].Reason
	})

	jsonOutputLocation, err := createEmptyFile(outputDirectory, fileName)
	if err != nil {
		return err
	}
	return marshalAndWriteJSON(providerStatistic, jsonOutputLocation)
}

// The fourth mode, which simply gives the provider name, and success percentage.
func (ce *coverageExportUtil) exportHumanReadable(outputDirectory string, fileName string) error {

	// The Coverage Tracker data structure is flattened to gather statistics about each language
	type LanguageStatistic struct {
		Total     int
		Successes int
	}

	type ProviderStatistic struct {
		Name             string
		Examples         int
		TotalConversions int
		Successes        int
	}

	// Main maps for holding the overall provider summary, and each language conversion statistic
	var allLanguageStatistics = make(map[string]*LanguageStatistic)
	var providerStatistic = ProviderStatistic{ce.Tracker.ProviderName, 0, 0, 0}

	// All the conversion attempts for each example are iterated by language name and
	// their results are added to the main map
	for _, exampleInMap := range ce.Tracker.EncounteredExamples {
		providerStatistic.Examples++
		for _, conversionResult := range exampleInMap.LanguagesConvertedTo {
			providerStatistic.TotalConversions++
			var language *LanguageStatistic
			if val, ok := allLanguageStatistics[conversionResult.TargetLanguage]; ok {

				// The main map already contains the language entry
				language = val
			} else {

				// The main map doesn't yet contain this language, and it needs to be added
				allLanguageStatistics[conversionResult.TargetLanguage] = &LanguageStatistic{0, 0}
				language = allLanguageStatistics[conversionResult.TargetLanguage]
			}

			// The language's entry in the summarized results is updated and any
			language.Total++
			if conversionResult.FailureSeverity == Success {
				providerStatistic.Successes++
				language.Successes++
			}
		}
	}

	targetFile, err := createEmptyFile(outputDirectory, fileName)
	if err != nil {
		return err
	}

	// Forming a string which will eventually be written to the target file
	fileString := fmt.Sprintf("Provider:     %s\nSuccess rate: %.2f%% (%d/%d)\n\n",
		providerStatistic.Name,
		float64(providerStatistic.Successes)/float64(providerStatistic.TotalConversions)*100.0,
		providerStatistic.Successes,
		providerStatistic.TotalConversions,
	)

	// Adding language results to the string in alphabetical order
	keys := make([]string, 0, len(allLanguageStatistics))
	for languageName := range allLanguageStatistics {
		keys = append(keys, languageName)
	}
	sort.Strings(keys)

	for _, languageName := range keys {
		languageStatistic := allLanguageStatistics[languageName]

		fileString += fmt.Sprintf("Converted %.2f%% of %s examples (%d/%d)\n",
			float64(languageStatistic.Successes)/float64(languageStatistic.Total)*100.0,
			languageName,
			languageStatistic.Successes,
			languageStatistic.Total,
		)
	}

	return ioutil.WriteFile(targetFile, []byte(fileString), 0600)
}

// Minor helper functions to assist with exporting results
func createEmptyFile(outputDirectory string, fileName string) (string, error) {
	outputLocation := filepath.Join(outputDirectory, fileName)
	err := os.MkdirAll(outputDirectory, 0700)
	return outputLocation, err
}

func marshalAndWriteJSON(unmarshalledData interface{}, finalDestination string) error {
	jsonBytes, err := json.MarshalIndent(unmarshalledData, "", "\t")
	if err != nil {
		return err
	}
	return ioutil.WriteFile(finalDestination, jsonBytes, 0600)
}
