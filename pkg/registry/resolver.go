package registry

import (
	"fmt"

	"github.com/openshift/ci-tools/pkg/api"
	"k8s.io/apimachinery/pkg/util/errors"
)

type Resolver interface {
	Resolve(config api.MultiStageTestConfiguration) (api.MultiStageTestConfigurationLiteral, error)
}

type ReferenceByName map[string]api.LiteralTestStep
type ChainByName map[string][]api.TestStep
type WorkflowByName map[string]api.MultiStageTestConfiguration

// registry will hold all the registry information needed to convert between the
// user provided configs referencing the registry and the internal, complete
// representation
type registry struct {
	stepsByName     ReferenceByName
	chainsByName    ChainByName
	workflowsByName WorkflowByName
}

func NewResolver(stepsByName ReferenceByName, chainsByName ChainByName, workflowsByName WorkflowByName) Resolver {
	return &registry{
		stepsByName:     stepsByName,
		chainsByName:    chainsByName,
		workflowsByName: workflowsByName,
	}
}

func (r *registry) Resolve(config api.MultiStageTestConfiguration) (api.MultiStageTestConfigurationLiteral, error) {
	var resolveErrors []error
	if config.Workflow != nil {
		workflow, ok := r.workflowsByName[*config.Workflow]
		if !ok {
			return api.MultiStageTestConfigurationLiteral{}, fmt.Errorf("no workflow named %s", *config.Workflow)
		}
		// is "" a valid cluster profile (for instance, can a user specify this for a random profile)?
		// if yes, we should change ClusterProfile to a pointer
		if config.ClusterProfile == "" {
			config.ClusterProfile = workflow.ClusterProfile
		}
		if config.Pre == nil {
			config.Pre = workflow.Pre
		}
		if config.Test == nil {
			config.Test = workflow.Test
		}
		if config.Post == nil {
			config.Post = workflow.Post
		}
	}
	expandedFlow := api.MultiStageTestConfigurationLiteral{
		ClusterProfile: config.ClusterProfile,
	}
	pre, errs := r.process(config.Pre)
	expandedFlow.Pre = append(expandedFlow.Pre, pre...)
	resolveErrors = append(resolveErrors, errs...)

	test, errs := r.process(config.Test)
	expandedFlow.Test = append(expandedFlow.Test, test...)
	resolveErrors = append(resolveErrors, errs...)

	post, errs := r.process(config.Post)
	expandedFlow.Post = append(expandedFlow.Post, post...)
	resolveErrors = append(resolveErrors, errs...)

	if resolveErrors != nil {
		return api.MultiStageTestConfigurationLiteral{}, errors.NewAggregate(resolveErrors)
	}
	return expandedFlow, nil
}

func (r *registry) process(steps []api.TestStep) (literalSteps []api.LiteralTestStep, errs []error) {
	// unroll chains
	var unrolledSteps []api.TestStep
	unrolledSteps, err := r.unrollChains(steps)
	if err != nil {
		errs = append(errs, err...)
	}
	// process steps
	for _, external := range unrolledSteps {
		var step api.LiteralTestStep
		if external.Reference != nil {
			var err error
			step, err = r.dereference(external)
			if err != nil {
				errs = append(errs, err)
			}
		} else if external.LiteralTestStep != nil {
			step = *external.LiteralTestStep
		} else {
			errs = append(errs, fmt.Errorf("encountered TestStep where both `Reference` and `LiteralTestStep` are nil"))
			continue
		}
		literalSteps = append(literalSteps, step)
	}
	if err := checkForDuplicates(literalSteps); err != nil {
		errs = append(errs, err...)
	}
	return
}

func (r *registry) unrollChains(input []api.TestStep) (unrolledSteps []api.TestStep, errs []error) {
	for _, step := range input {
		if step.Chain != nil {
			chain, ok := r.chainsByName[*step.Chain]
			if !ok {
				return []api.TestStep{}, []error{fmt.Errorf("unknown step chain: %s", *step.Chain)}
			}
			// handle nested chains
			chain, err := r.unrollChains(chain)
			if err != nil {
				errs = append(errs, err...)
			}
			unrolledSteps = append(unrolledSteps, chain...)
			continue
		}
		unrolledSteps = append(unrolledSteps, step)
	}
	return
}

func (r *registry) dereference(input api.TestStep) (api.LiteralTestStep, error) {
	step, ok := r.stepsByName[*input.Reference]
	if !ok {
		return api.LiteralTestStep{}, fmt.Errorf("invalid step reference: %s", *input.Reference)
	}
	return step, nil
}

func checkForDuplicates(input []api.LiteralTestStep) (errs []error) {
	seen := make(map[string]bool)
	for _, step := range input {
		_, ok := seen[step.As]
		if ok {
			errs = append(errs, fmt.Errorf("duplicate name: %s", step.As))
		}
		seen[step.As] = true
	}
	return
}

// ResolveConfig uses a resolver to resolve an entire ci-operator config
func ResolveConfig(resolver Resolver, config api.ReleaseBuildConfiguration) (api.ReleaseBuildConfiguration, error) {
	var resolvedTests []api.TestStepConfiguration
	for _, step := range config.Tests {
		// no changes if step is not multi-stage
		if step.MultiStageTestConfiguration == nil {
			resolvedTests = append(resolvedTests, step)
			continue
		}
		resolvedConfig, err := resolver.Resolve(*step.MultiStageTestConfiguration)
		if err != nil {
			return api.ReleaseBuildConfiguration{}, fmt.Errorf("Failed resolve MultiStageTestConfiguration: %v", err)
		}
		step.MultiStageTestConfigurationLiteral = &resolvedConfig
		// remove old multi stage config
		step.MultiStageTestConfiguration = nil
		resolvedTests = append(resolvedTests, step)
	}
	config.Tests = resolvedTests
	return config, nil
}
