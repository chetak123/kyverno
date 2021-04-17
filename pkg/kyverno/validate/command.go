package validate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"

	v1 "github.com/kyverno/kyverno/pkg/api/kyverno/v1"
	"github.com/kyverno/kyverno/pkg/kyverno/common"
	sanitizederror "github.com/kyverno/kyverno/pkg/kyverno/sanitizedError"
	"github.com/kyverno/kyverno/pkg/openapi"
	policy2 "github.com/kyverno/kyverno/pkg/policy"
	"github.com/kyverno/kyverno/pkg/utils"
	"github.com/spf13/cobra"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	yaml1 "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"
)

// Command returns validate command
func Command() *cobra.Command {
	var outputType string
	var crdPaths []string
	cmd := &cobra.Command{
		Use:     "validate",
		Short:   "Validates kyverno policies",
		Example: "kyverno validate /path/to/policy.yaml /path/to/folderOfPolicies",
		RunE: func(cmd *cobra.Command, policyPaths []string) (err error) {
			defer func() {
				if err != nil {
					if !sanitizederror.IsErrorSanitized(err) {
						log.Log.Error(err, "failed to sanitize")
						err = fmt.Errorf("internal error")
					}
				}
			}()

			if outputType != "" {
				if outputType != "yaml" && outputType != "json" {
					return sanitizederror.NewWithError(fmt.Sprintf("%s format is not supported", outputType), errors.New("yaml and json are supported"))
				}
			}

			if len(policyPaths) == 0 {
				return sanitizederror.NewWithError(fmt.Sprintf("policy file(s) required"), err)
			}

			var policies []*v1.ClusterPolicy
			var errs []error
			if policyPaths[0] == "-" {
				if common.IsInputFromPipe() {
					policyStr := ""
					scanner := bufio.NewScanner(os.Stdin)
					for scanner.Scan() {
						policyStr = policyStr + scanner.Text() + "\n"
					}

					yamlBytes := []byte(policyStr)
					policies, err = utils.GetPolicy(yamlBytes)
					if err != nil {
						return sanitizederror.NewWithError("failed to parse policy", err)
					}
				}
			} else {
				policies, errs = common.GetPolicies(policyPaths)
				if len(errs) > 0 && len(policies) == 0 {
					return sanitizederror.NewWithErrors("failed to read policies", errs)
				}

				if len(errs) > 0 && log.Log.V(1).Enabled() {
					fmt.Printf("ignoring errors: \n")
					for _, e := range errs {
						fmt.Printf("    %v \n", e.Error())
					}
				}
			}

			openAPIController, err := openapi.NewOpenAPIController()
			if err != nil {
				return sanitizederror.NewWithError("failed to initialize openAPIController", err)
			}

			crdPath := "../crds/policy.yaml"
			var v1crd apiextensions.CustomResourceDefinitionSpec
			crdBytes := convertToJSONbytes(crdPath)
			if err := json.Unmarshal(crdBytes, &v1crd); err != nil {
				fmt.Println("failed to decode crd: ", err)
			}

			// if CRD's are passed, add these to OpenAPIController
			if len(crdPaths) > 0 {
				crds, err := common.GetCRDs(crdPaths)
				if err != nil {
					fmt.Printf("\nError: crd is invalid. \nFile: %s \nCause: %s\n", crdPaths, err)
					os.Exit(1)
				}
				for _, crd := range crds {
					openAPIController.ParseCRD(*crd)
				}
			}

			invalidPolicyFound := false
			for _, policy := range policies {
				fmt.Println("----------------------------------------------------------------------")
				validatePolicyAccordingToPolicyCRD(policy, v1crd)
				err := policy2.Validate(policy, nil, true, openAPIController)
				if err != nil {
					fmt.Printf("Policy %s is invalid.\n", policy.Name)
					fmt.Printf("Error: invalid policy.\nCause: %s\n\n", err)
					invalidPolicyFound = true
				} else {
					fmt.Printf("Policy %s is valid.\n\n", policy.Name)
					if outputType != "" {
						logger := log.Log.WithName("validate")
						p, err := common.MutatePolicy(policy, logger)
						if err != nil {
							if !sanitizederror.IsErrorSanitized(err) {
								return sanitizederror.NewWithError("failed to mutate policy.", err)
							}
							return err
						}
						if outputType == "yaml" {
							yamlPolicy, _ := yaml.Marshal(p)
							fmt.Println(string(yamlPolicy))
						} else {
							jsonPolicy, _ := json.MarshalIndent(p, "", "  ")
							fmt.Println(string(jsonPolicy))
						}
					}
				}
			}

			if invalidPolicyFound == true {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&outputType, "output", "o", "", "Prints the mutated policy in yaml or json format")
	cmd.Flags().StringArrayVarP(&crdPaths, "crd", "c", []string{}, "Path to CRD files")
	return cmd
}

func convertToJSONbytes(path string) []byte {
	pathBytes, err := ioutil.ReadFile(path)
	if err != nil {
		println("error in extracting in bytes: ", err)
	}
	jsonBytes, err := yaml1.ToJSON(pathBytes)
	if err != nil {
		fmt.Printf("failed to convert to JSON: %v\n", err)
	}
	return jsonBytes
}

func validatePolicyAccordingToPolicyCRD(policy *v1.ClusterPolicy, v1crd apiextensions.CustomResourceDefinitionSpec) {
	policyBytes, err := json.Marshal(policy)
	if err != nil {
		fmt.Println("failed to marshal policy. error: ", err)
	}

	u := &unstructured.Unstructured{}
	err = u.UnmarshalJSON(policyBytes)
	if err != nil {
		fmt.Println("failed to decode policy", err)
	}

	versions := v1crd.Versions
	for _, version := range versions {
		validator, _, err := apiservervalidation.NewSchemaValidator(&apiextensions.CustomResourceValidation{OpenAPIV3Schema: version.Schema.OpenAPIV3Schema})
		if err != nil {
			fmt.Println("failed to create schema validator", err)
		}

		errList := apiservervalidation.ValidateCustomResource(nil, u.UnstructuredContent(), validator)
		if errList != nil {
			fmt.Println(errList)
		}
	}
}
