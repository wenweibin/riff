/*
 * Copyright 2019 the original author or authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/projectriff/riff/cli/pkg/cli"
	"github.com/projectriff/riff/cli/pkg/cli/options"
	"github.com/projectriff/riff/cli/pkg/k8s"
	"github.com/projectriff/riff/cli/pkg/parsers"
	"github.com/projectriff/riff/cli/pkg/race"
	"github.com/projectriff/riff/cli/pkg/validation"
	knativev1alpha1 "github.com/projectriff/riff/system/pkg/apis/knative/v1alpha1"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type DeployerCreateOptions struct {
	options.ResourceOptions

	Image          string
	ApplicationRef string
	ContainerRef   string
	FunctionRef    string

	IngressPolicy string
	TargetPort    int32

	ContainerConcurrency int64

	Env     []string
	EnvFrom []string

	LimitCPU    string
	LimitMemory string

	MaxScale int32
	MinScale int32

	Tail        bool
	WaitTimeout string

	DryRun bool
}

var (
	_ cli.Validatable = (*DeployerCreateOptions)(nil)
	_ cli.Executable  = (*DeployerCreateOptions)(nil)
	_ cli.DryRunable  = (*DeployerCreateOptions)(nil)
)

func (opts *DeployerCreateOptions) Validate(ctx context.Context) cli.FieldErrors {
	errs := cli.FieldErrors{}

	errs = errs.Also(opts.ResourceOptions.Validate(ctx))

	// application-ref, build-ref and image are mutually exclusive
	used := []string{}
	unused := []string{}

	if opts.ApplicationRef != "" {
		used = append(used, cli.ApplicationRefFlagName)
	} else {
		unused = append(unused, cli.ApplicationRefFlagName)
	}

	if opts.ContainerRef != "" {
		used = append(used, cli.ContainerRefFlagName)
	} else {
		unused = append(unused, cli.ContainerRefFlagName)
	}

	if opts.FunctionRef != "" {
		used = append(used, cli.FunctionRefFlagName)
	} else {
		unused = append(unused, cli.FunctionRefFlagName)
	}

	if opts.Image != "" {
		used = append(used, cli.ImageFlagName)
	} else {
		unused = append(unused, cli.ImageFlagName)
	}

	if len(used) == 0 {
		errs = errs.Also(cli.ErrMissingOneOf(unused...))
	} else if len(used) > 1 {
		errs = errs.Also(cli.ErrMultipleOneOf(used...))
	}

	if opts.IngressPolicy != string(knativev1alpha1.IngressPolicyClusterLocal) && opts.IngressPolicy != string(knativev1alpha1.IngressPolicyExternal) {
		errs = errs.Also(cli.ErrInvalidValue(opts.IngressPolicy, cli.IngressPolicyFlagName))
	}

	errs = errs.Also(validation.ContainerConcurrency(opts.ContainerConcurrency, cli.ContainerConcurrencyFlagName))

	errs = errs.Also(validation.EnvVars(opts.Env, cli.EnvFlagName))
	errs = errs.Also(validation.EnvVarFroms(opts.EnvFrom, cli.EnvFromFlagName))

	if opts.LimitCPU != "" {
		errs = errs.Also(validation.Quantity(opts.LimitCPU, cli.LimitCPUFlagName))
	}
	if opts.LimitMemory != "" {
		errs = errs.Also(validation.Quantity(opts.LimitMemory, cli.LimitMemoryFlagName))
	}

	if opts.MinScale < int32(0) {
		errs = errs.Also(cli.ErrInvalidValue(opts.MinScale, cli.MinScaleFlagName))
	}

	if cmd := cli.CommandFromContext(ctx); cmd != nil {
		if cmd.Flags().Changed(cli.StripDash(cli.MaxScaleFlagName)) && opts.MaxScale < int32(1) {
			errs = errs.Also(cli.ErrInvalidValue(opts.MaxScale, cli.MaxScaleFlagName))
		}
	}

	if opts.MaxScale > int32(0) && opts.MinScale > opts.MaxScale {
		errs = errs.Also(cli.ErrInvalidValue(opts.MaxScale, cli.MaxScaleFlagName))
	}

	if opts.TargetPort != 0 {
		errs = errs.Also(validation.PortNumber(opts.TargetPort, cli.TargetPortFlagName))
	}

	if opts.Tail {
		if opts.WaitTimeout == "" {
			errs = errs.Also(cli.ErrMissingField(cli.WaitTimeoutFlagName))
		} else if _, err := time.ParseDuration(opts.WaitTimeout); err != nil {
			errs = errs.Also(cli.ErrInvalidValue(opts.WaitTimeout, cli.WaitTimeoutFlagName))
		}
	}

	if opts.DryRun && opts.Tail {
		errs = errs.Also(cli.ErrMultipleOneOf(cli.DryRunFlagName, cli.TailFlagName))
	}

	return errs
}

func (opts *DeployerCreateOptions) Exec(ctx context.Context, c *cli.Config) error {
	deployer := &knativev1alpha1.Deployer{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: opts.Namespace,
			Name:      opts.Name,
		},
		Spec: knativev1alpha1.DeployerSpec{
			Template: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{}},
				},
			},
			IngressPolicy: knativev1alpha1.IngressPolicy(opts.IngressPolicy),
		},
	}

	cmd := cli.CommandFromContext(ctx)

	if opts.ApplicationRef != "" {
		deployer.Spec.Build = &knativev1alpha1.Build{
			ApplicationRef: opts.ApplicationRef,
		}
	}
	if opts.ContainerRef != "" {
		deployer.Spec.Build = &knativev1alpha1.Build{
			ContainerRef: opts.ContainerRef,
		}
	}
	if opts.FunctionRef != "" {
		deployer.Spec.Build = &knativev1alpha1.Build{
			FunctionRef: opts.FunctionRef,
		}
	}
	if opts.Image != "" {
		deployer.Spec.Template.Spec.Containers[0].Image = opts.Image
	}

	if cmd != nil && cmd.Flags().Changed(cli.StripDash(cli.ContainerConcurrencyFlagName)) {
		deployer.Spec.ContainerConcurrency = &opts.ContainerConcurrency
	}

	for _, env := range opts.Env {
		if deployer.Spec.Template.Spec.Containers[0].Env == nil {
			deployer.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{}
		}
		deployer.Spec.Template.Spec.Containers[0].Env = append(deployer.Spec.Template.Spec.Containers[0].Env, parsers.EnvVar(env))
	}
	for _, env := range opts.EnvFrom {
		if deployer.Spec.Template.Spec.Containers[0].Env == nil {
			deployer.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{}
		}
		deployer.Spec.Template.Spec.Containers[0].Env = append(deployer.Spec.Template.Spec.Containers[0].Env, parsers.EnvVarFrom(env))
	}

	if (opts.LimitCPU != "" || opts.LimitMemory != "") && deployer.Spec.Template.Spec.Containers[0].Resources.Limits == nil {
		deployer.Spec.Template.Spec.Containers[0].Resources.Limits = corev1.ResourceList{}
	}
	if opts.LimitCPU != "" {
		// parse errors are handled by the opt validation
		deployer.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU] = resource.MustParse(opts.LimitCPU)
	}
	if opts.LimitMemory != "" {
		// parse errors are handled by the opt validation
		deployer.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse(opts.LimitMemory)
	}
	if opts.TargetPort > 0 {
		deployer.Spec.Template.Spec.Containers[0].Ports = []corev1.ContainerPort{
			{Protocol: corev1.ProtocolTCP, ContainerPort: opts.TargetPort},
		}
	}
	if opts.MaxScale > 0 {
		deployer.Spec.Scale.Max = &opts.MaxScale
	}
	if cmd != nil && cmd.Flags().Changed(cli.StripDash(cli.MinScaleFlagName)) {
		deployer.Spec.Scale.Min = &opts.MinScale
	}

	if opts.DryRun {
		cli.DryRunResource(ctx, deployer, deployer.GetGroupVersionKind())
	} else {
		var err error
		deployer, err = c.KnativeRuntime().Deployers(opts.Namespace).Create(deployer)
		if err != nil {
			return err
		}
	}
	c.Successf("Created deployer %q\n", deployer.Name)
	if opts.Tail {
		c.Infof("Waiting for deployer %q to become ready...\n", deployer.Name)
		// err guarded by Validate()
		timeout, _ := time.ParseDuration(opts.WaitTimeout)
		err := race.Run(ctx, timeout,
			func(ctx context.Context) error {
				return k8s.WaitUntilReady(ctx, c.KnativeRuntime().RESTClient(), "deployers", deployer)
			},
			func(ctx context.Context) error {
				return c.Kail.KnativeDeployerLogs(ctx, deployer, cli.TailSinceCreateDefault, c.Stdout)
			},
		)
		if err == context.DeadlineExceeded {
			c.Errorf("Timeout after %q waiting for %q to become ready\n", opts.WaitTimeout, opts.Name)
			c.Infof("To view status run: %s knative deployer list %s %s\n", c.Name, cli.NamespaceFlagName, opts.Namespace)
			c.Infof("To continue watching logs run: %s knative deployer tail %s %s %s\n", c.Name, opts.Name, cli.NamespaceFlagName, opts.Namespace)
			err = cli.SilenceError(err)
		}
		if err != nil {
			return err
		}
		c.Successf("Deployer %q is ready\n", deployer.Name)
	}
	return nil
}

func (opts *DeployerCreateOptions) IsDryRun() bool {
	return opts.DryRun
}

func NewDeployerCreateCommand(ctx context.Context, c *cli.Config) *cobra.Command {
	opts := &DeployerCreateOptions{}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "create a deployer to map HTTP requests to a workload",
		Long: strings.TrimSpace(`
Create a Knative deployer.

Build references are resolved within the same namespace as the deployer. As the
build produces new images, the image will roll out automatically. Image based
deployers must be updated manually to roll out new images. Rolling out an image
means creating a Knative Configuration with a pod referencing the image and a
Knative Route referencing the Configuration.

The runtime environment can be configured by ` + cli.EnvFlagName + ` for static key-value pairs
and ` + cli.EnvFromFlagName + ` to map values from a ConfigMap or Secret.
`),
		Example: strings.Join([]string{
			fmt.Sprintf("%s knative deployer create my-app-deployer %s my-app", c.Name, cli.ApplicationRefFlagName),
			fmt.Sprintf("%s knative deployer create my-func-deployer %s my-func", c.Name, cli.FunctionRefFlagName),
			fmt.Sprintf("%s knative deployer create my-func-deployer %s my-container", c.Name, cli.ContainerRefFlagName),
			fmt.Sprintf("%s knative deployer create my-image-deployer %s registry.example.com/my-image:latest", c.Name, cli.ImageFlagName),
		}, "\n"),
		PreRunE: cli.ValidateOptions(ctx, opts),
		RunE:    cli.ExecOptions(ctx, c, opts),
	}

	cli.Args(cmd,
		cli.NameArg(&opts.Name),
	)

	cli.NamespaceFlag(cmd, c, &opts.Namespace)
	cmd.Flags().StringVar(&opts.Image, cli.StripDash(cli.ImageFlagName), "", "container `image` to deploy")
	cmd.Flags().StringVar(&opts.ApplicationRef, cli.StripDash(cli.ApplicationRefFlagName), "", "`name` of application to deploy")
	_ = cmd.MarkFlagCustom(cli.StripDash(cli.ApplicationRefFlagName), "__"+c.Name+"_list_applications")
	cmd.Flags().StringVar(&opts.ContainerRef, cli.StripDash(cli.ContainerRefFlagName), "", "`name` of container to deploy")
	_ = cmd.MarkFlagCustom(cli.StripDash(cli.ContainerRefFlagName), "__"+c.Name+"_list_containers")
	cmd.Flags().StringVar(&opts.FunctionRef, cli.StripDash(cli.FunctionRefFlagName), "", "`name` of function to deploy")
	_ = cmd.MarkFlagCustom(cli.StripDash(cli.FunctionRefFlagName), "__"+c.Name+"_list_functions")
	cmd.Flags().StringVar(&opts.IngressPolicy, cli.StripDash(cli.IngressPolicyFlagName), string(knativev1alpha1.IngressPolicyClusterLocal), fmt.Sprintf("ingress `policy` for network access to the workload, one of %q or %q", knativev1alpha1.IngressPolicyClusterLocal, knativev1alpha1.IngressPolicyExternal))
	_ = cmd.MarkFlagCustom(cli.StripDash(cli.IngressPolicyFlagName), "__"+c.Name+"_ingress_policy")
	cmd.Flags().Int64Var(&opts.ContainerConcurrency, cli.StripDash(cli.ContainerConcurrencyFlagName), 0, "the maximum `number` of concurrent requests to send to a replica at one time")
	cmd.Flags().StringArrayVar(&opts.Env, cli.StripDash(cli.EnvFlagName), []string{}, fmt.Sprintf("environment `variable` defined as a key value pair separated by an equals sign, example %q (may be set multiple times)", fmt.Sprintf("%s MY_VAR=my-value", cli.EnvFlagName)))
	cmd.Flags().StringArrayVar(&opts.EnvFrom, cli.StripDash(cli.EnvFromFlagName), []string{}, fmt.Sprintf("environment `variable` from a config map or secret, example %q, %q (may be set multiple times)", fmt.Sprintf("%s MY_SECRET_VALUE=secretKeyRef:my-secret-name:key-in-secret", cli.EnvFromFlagName), fmt.Sprintf("%s MY_CONFIG_MAP_VALUE=configMapKeyRef:my-config-map-name:key-in-config-map", cli.EnvFromFlagName)))
	cmd.Flags().StringVar(&opts.LimitCPU, cli.StripDash(cli.LimitCPUFlagName), "", "the maximum amount of cpu allowed, in CPU `cores` (500m = .5 cores)")
	cmd.Flags().StringVar(&opts.LimitMemory, cli.StripDash(cli.LimitMemoryFlagName), "", "the maximum amount of memory allowed, in `bytes` (500Mi = 500MiB = 500 * 1024 * 1024)")
	cmd.Flags().Int32Var(&opts.MaxScale, cli.StripDash(cli.MaxScaleFlagName), int32(0), "maximum `number` of replicas (default unbounded)")
	cmd.Flags().Int32Var(&opts.MinScale, cli.StripDash(cli.MinScaleFlagName), int32(0), "minimum `number` of replicas (default 0)")
	cmd.Flags().BoolVar(&opts.Tail, cli.StripDash(cli.TailFlagName), false, "watch deployer logs")
	cmd.Flags().StringVar(&opts.WaitTimeout, cli.StripDash(cli.WaitTimeoutFlagName), "10m", "`duration` to wait for the deployer to become ready when watching logs")
	cmd.Flags().BoolVar(&opts.DryRun, cli.StripDash(cli.DryRunFlagName), false, "print kubernetes resources to stdout rather than apply them to the cluster, messages normally on stdout will be sent to stderr")
	cmd.Flags().Int32Var(&opts.TargetPort, cli.StripDash(cli.TargetPortFlagName), 0, "`port` that the workload listens on for traffic. The value is exposed to the workload as the PORT environment variable")

	return cmd
}