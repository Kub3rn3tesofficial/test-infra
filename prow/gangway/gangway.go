/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gangway

import (
	context "context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/sirupsen/logrus"
	codes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	status "google.golang.org/grpc/status"
	coreapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	prowcrd "k8s.io/test-infra/prow/apis/prowjobs/v1"
	"k8s.io/test-infra/prow/config"
	"k8s.io/test-infra/prow/logrusutil"
	"k8s.io/test-infra/prow/pjutil"
	"k8s.io/test-infra/prow/version"
)

const (
	HEADER_API_CONSUMER_TYPE = "x-endpoint-api-consumer-type"
	HEADER_API_CONSUMER_ID   = "x-endpoint-api-consumer-number"
)

type Gangway struct {
	UnimplementedProwServer
	ConfigAgent              *config.Agent
	ProwJobClient            ProwJobClient
	InRepoConfigCacheHandler *config.InRepoConfigCacheHandler
}

// ProwJobClient is mostly for testing (for calling into the low-level
// Kubernetes API to check whether gangway behaved correctly).
type ProwJobClient interface {
	Create(context.Context, *prowcrd.ProwJob, metav1.CreateOptions) (*prowcrd.ProwJob, error)
}

func (gw *Gangway) CreateJobExecution(ctx context.Context, req *CreateJobExecutionRequest) (*JobExecution, error) {
	err, md := getHttpRequestHeaders(ctx)
	if err != nil {
		logrus.WithError(err).Error("could not find request HTTP headers")
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Validate request fields.
	if err := req.Validate(); err != nil {
		logrus.WithError(err).Error("could not validate request fields")
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// FIXME (listx) Add execution token generation database call, so that we can
	// reduce the delay between the initial call and the creation of the ProwJob
	// CR. We should probably use UUIDv7 (see
	// https://www.ietf.org/archive/id/draft-peabody-dispatch-new-uuid-format-01.html).
	// Also see FireBase's PushID for comparison:
	// https://firebase.blog/posts/2015/02/the-2120-ways-to-ensure-unique_68.

	// Identify the client from the request metadata.
	mainConfig := gw.ConfigAgent.Config()
	allowedAPIClient, err := IdentifyAllowedClient(mainConfig, md)
	if err != nil {
		logrus.WithError(err).Error("could not find client in allowlist")
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	enableDecoratedLogger(allowedAPIClient, md)

	// At this point we know that this request is authorized (the request has
	// GCP-specific headers, and the headers point to an allowlisted client ID).
	// Now we need to check whether this authenticated API client has
	// authorization to trigger the requested Prow Job.
	authorized, err := ClientAuthorized(allowedAPIClient, mainConfig, req)
	if err != nil {
		logrus.WithError(err).Error("failed to determine client authorization")
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if !authorized {
		logrus.Error("client is not authorized to execute the given job")
		return nil, status.Error(codes.PermissionDenied, "client is not authorized to execute the given job")
	}

	// Fetch the job definition. We can use most of the existing code in Sub for
	// this. The key is to translate the existing data from the request into
	// something that the codebase understands (e.g., "pulls" instead of
	// "refsToMerge").

	jobStruct, err := gw.FetchJobStruct(req)
	if err != nil {
		logrus.WithError(err).Error("could not find requested job config")
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	var ok bool
	var periodic config.Periodic
	var presubmit config.Presubmit
	var postsubmit config.Postsubmit
	var spec prowcrd.ProwJobSpec
	var refs *prowcrd.Refs

	// Construct "prowcrd.Refs" type which encodes the Git references we want to
	// clone/merge at runtime. We get this information from the request, as well
	// as the Periodic/Presubmut/Postsubmit struct's JobBase.ExtraRefs (which
	// has type prowcrd.Refs).
	gitRefs := req.GetGitRefs()
	refs, err = MkRefs(gitRefs)
	if err != nil {
		logrus.WithError(err).Error("could not construct refs from baseRepo")
		return nil, err
	}

	// Coerce jobStruct into either a Presubmit, Postsubmit, or Periodic type, based on the
	switch jobStruct.JobExecutionType {
	case JobExecutionType_PERIODIC:
		periodic, ok = (*jobStruct.Job).(config.Periodic)
		if !ok {
			msg := "could not coerce jobStruct.Job into Periodic"
			logrus.Error(msg)
			return nil, status.Error(codes.Internal, msg)
		}
		// We don't allow periodic jobs to clone a base repo. This is mainly
		// because we're using the underlying pjutil.PeriodicSpec() function
		// which doesn't take a "refs" argument.
		spec = pjutil.PeriodicSpec(periodic)
	case JobExecutionType_POSTSUBMIT:
		postsubmit, ok = (*jobStruct.Job).(config.Postsubmit)
		if !ok {
			msg := "could not coerce jobStruct.Job into Postsubmit"
			logrus.Error(msg)
			return nil, status.Error(codes.Internal, msg)
		}
		spec = pjutil.PostsubmitSpec(postsubmit, *refs)
	case JobExecutionType_PRESUBMIT:
		presubmit, ok = (*jobStruct.Job).(config.Presubmit)
		if !ok {
			msg := "could not coerce jobStruct.Job into Presubmit"
			logrus.Error(msg)
			return nil, status.Error(codes.Internal, msg)
		}
		spec = pjutil.PresubmitSpec(presubmit, *refs)
	}

	// Inject labels, annotations, and envs into the job.
	podSpecOptions := req.GetPodSpecOptions()
	labels := make(map[string]string)
	annotations := make(map[string]string)
	if podSpecOptions != nil {
		psoLabels := podSpecOptions.GetLabels()
		for k, v := range psoLabels {
			labels[k] = v
		}
		psoAnnotations := podSpecOptions.GetAnnotations()
		for k, v := range psoAnnotations {
			annotations[k] = v
		}
	}
	prowJobCR := pjutil.NewProwJob(spec, labels, annotations)
	if prowJobCR.Spec.PodSpec != nil {
		if podSpecOptions != nil {
			envs := podSpecOptions.GetEnvs()
			if envs != nil {
				for i, c := range prowJobCR.Spec.PodSpec.Containers {
					for k, v := range envs {
						c.Env = append(c.Env, coreapi.EnvVar{Name: k, Value: v})
					}
					prowJobCR.Spec.PodSpec.Containers[i].Env = c.Env
				}
			}
		}
	}

	if _, err := gw.ProwJobClient.Create(context.TODO(), &prowJobCR, metav1.CreateOptions{}); err != nil {
		logrus.WithError(err).Errorf("failed to create job %q as %q", req.GetJobName(), prowJobCR.Name)
		return nil, err
	} else {
		logrus.Infof("created Prow Job %s", prowJobCR.Name)
	}

	// Now populate a JobExecution. We have to convert data from the ProwJob
	// custom resource to a JobExecution. For now we just reuse the "Name" field
	// of a ProwJob CR as a globally-unique execution ID, because this existing
	// string is already used to do lookups on Deck
	// (https://prow.k8s.io/prowjob?prowjob=c2891365-621c-11ed-88b0-da2d50b4915c)
	// but also for naming the test pod itself (prowcrd.ProwJob.Status.pod_name
	// field).
	return &JobExecution{
		Id:     prowJobCR.Name,
		Status: JobExecutionStatus_TRIGGERED,
	}, nil
}

func getOrgRepo(url string) (string, string, error) {
	urlNormalized := strings.TrimSuffix(url, "/")
	if !strings.Contains(urlNormalized, "/") {
		return "", "", fmt.Errorf("url %q does not contain a slash", urlNormalized)
	}
	parts := strings.Split(urlNormalized, "/")
	repo := parts[len(parts)-1]
	if repo == "" {
		return "", "", fmt.Errorf("url %q has an empty repo", url)
	}
	org := strings.Join(parts[0:len(parts)-1], "/")
	if org == "" {
		return "", "", fmt.Errorf("url %q has an empty org", url)
	}

	// Special-case GitHub. This is due to legacy reasons --- originally we only
	// had GitHub and so did not even encode the leading prefix starting with
	// "https://..."; e.g., for "https://github.com/myorg/myrepo", the org and
	// repo were just "myorg" and "myrepo". But then Gerrit support came along
	// and we needed to namespace Gerrit repos from GitHub ones, and the way we
	// did this is to encode the leading URL bit for Gerrit repos. See
	// TestGetOrgRepo for expected behavior.
	gitHubPrefix := "https://github.com/"

	org = strings.TrimPrefix(org, gitHubPrefix)

	return org, repo, nil
}

// IdentifyAllowedClient looks at the HTTP request headers (metadata) and tries
// to match it up with an allowlisted Client already defined in the main Config.
//
// Each supported client.Type has custom logic around the HTTP metadata headers
// to know what kind of headers to look for. Different cloud vendors will have
// different HTTP metdata headers, although technically nothing stops users from
// injecting these headers manually on their own.
func IdentifyAllowedClient(c *config.Config, md *metadata.MD) (*config.AllowedAPIClient, error) {
	if md == nil {
		return nil, errors.New("metadata cannot be nil")
	}

	if c == nil {
		return nil, errors.New("config cannot be nil")
	}

	for _, client := range c.AllowedAPIClients {
		switch client.Type {
		case "GCP_PROJECT":
			// First check that the expected headers even exist (and are formatted correctly).
			err := assertRequiredHeaders(md, []string{"x-endpoint-api-consumer-type", "x-endpoint-api-consumer-number"})
			if err != nil {
				return nil, err
			}

			v := md.Get("x-endpoint-api-consumer-type")[0]
			if v != "PROJECT" {
				return nil, fmt.Errorf("unsupported GCP API consumer type: %q", v)
			}
			v = md.Get("x-endpoint-api-consumer-number")[0]

			// Now check whether we can find the same information in the Config's allowlist.
			if client.ID == v {
				return client, nil
			}
		}
	}

	return nil, fmt.Errorf("could not find allowed client from %v", md)
}

// ClientAuthorized checks whether or not a client can run a Prow job based on
// the job's identifier (is this client allowed to run jobs meant for the given
// identifier?). This needs to traverse the config to determine whether the
// allowlist (allowed_api_clients) allows it.
func ClientAuthorized(allowedAPIClient *config.AllowedAPIClient, c *config.Config, req *CreateJobExecutionRequest) (bool, error) {
	switch req.GetJobExecutionType() {
	// Skip job authorization for Periodic jobs, because they are not associated with any repo.
	case JobExecutionType_PERIODIC:
		return true, nil
	case JobExecutionType_POSTSUBMIT:
		fallthrough
	case JobExecutionType_PRESUBMIT:
		gitRefs := req.GetGitRefs()
		requestedOrg, requestedRepo, err := getOrgRepo(gitRefs.GetBase().GetUrl())
		if err != nil {
			return false, err
		}
		requestedOrgRepo := fmt.Sprintf("%s/%s", requestedOrg, requestedRepo)

		for _, job_subset := range allowedAPIClient.AllowedJobSubsets {
			// Need to check if identifier falls under the job_subset.Org +
			// job_subset.Repo.
			allowedOrgRepo := fmt.Sprintf("%s/%s", job_subset.Org, job_subset.Repo)

			if requestedOrgRepo == allowedOrgRepo {
				return true, nil
			}

			// Allow wildcard for repos.
			if job_subset.Repo == "*" {
				if job_subset.Org == requestedOrg {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func MkRefs(grd *GitReferenceDynamic) (*prowcrd.Refs, error) {
	var refs prowcrd.Refs
	if grd == nil {
		return &refs, nil
	}

	var pulls []prowcrd.Pull
	base := grd.GetBase()
	refsToMerge := grd.GetRefsToMerge()

	url := base.GetUrl()
	commit := base.GetCommit()
	ref := base.GetRef()

	org, repo, err := getOrgRepo(url)
	if err != nil {
		return nil, err
	}

	// Convert GitReferenceDynamic into prowcrd.Refs.
	refs = prowcrd.Refs{
		Org:      org,
		Repo:     repo,
		RepoLink: url,
		BaseRef:  ref,
		BaseSHA:  commit,
	}

	// Convert refsToMerge to "pulls" (refToMerge -> prowcrd.Pull).
	for _, refToMerge := range refsToMerge {
		// prowcrd.Pull does not require "orgRepo" information (the various
		// *Link fields are for reporting, not for cloning), because it is
		// implied from the orgRepo (URL) of prowcrd.Refs.
		pull := prowcrd.Pull{
			SHA: refToMerge.GetCommit(),
			Ref: refToMerge.GetRef(),
		}
		pulls = append(pulls, pull)
	}

	refs.Pulls = pulls

	return &refs, nil
}

type JobStruct struct {
	Job *interface{}
	// Encode the type of the Job to coerce into (via type assertion), so that
	// users of JobStruct know how to make sense of the Job details.
	JobExecutionType JobExecutionType
}

// We have to pay the cost of code duplication for Presubmits vs Postsubmits
// because although they are different types they behave almost the same way for
// purposes of fetching their job definitions. Either we duplicate a lot of the
// same business logic, or we duplicate a lot of lower-level "infra" logic. We
// choose to do the latter here. For an approach of duplicating the business
// logic (which is less desirable), see the code for the "subscriber" package
// used by the Sub component, specifically getProwJobSpec().
type presubmitFetchers struct{}
type postsubmitFetchers struct{}

type jobFetchers interface {
	getJobsFromStaticConfig(cfg *config.Config, orgRepo string) []interface{}
	getJobsFromInRepoConfig(cfg *config.Config, pc *config.InRepoConfigCacheHandler, orgRepo string, baseSHAGetter config.RefGetter, headSHAGetters ...config.RefGetter) ([]interface{}, error)
	couldRun(job interface{}, ref string) bool
	name(job interface{}) string
}

func (jf *presubmitFetchers) getJobsFromStaticConfig(cfg *config.Config, orgRepo string) []interface{} {
	jobs := cfg.GetPresubmitsStatic(orgRepo)
	ret := make([]interface{}, len(jobs))
	for i, v := range jobs {
		ret[i] = v
	}
	return ret
}

func (jf *postsubmitFetchers) getJobsFromStaticConfig(cfg *config.Config, orgRepo string) []interface{} {
	jobs := cfg.GetPostsubmitsStatic(orgRepo)
	ret := make([]interface{}, len(jobs))
	for i, v := range jobs {
		ret[i] = v
	}
	return ret
}

func (jf *presubmitFetchers) couldRun(job interface{}, ref string) bool {
	typedJob := job.(config.Presubmit)
	return typedJob.CouldRun(ref)
}

func (jf *postsubmitFetchers) couldRun(job interface{}, ref string) bool {
	typedJob := job.(config.Postsubmit)
	return typedJob.CouldRun(ref)
}

func (jf *presubmitFetchers) name(job interface{}) string {
	typedJob := job.(config.Presubmit)
	return typedJob.Name
}

func (jf *postsubmitFetchers) name(job interface{}) string {
	typedJob := job.(config.Postsubmit)
	return typedJob.Name
}

func (jf *presubmitFetchers) getJobsFromInRepoConfig(cfg *config.Config, pc *config.InRepoConfigCacheHandler, orgRepo string, baseSHAGetter config.RefGetter, headSHAGetters ...config.RefGetter) ([]interface{}, error) {
	jobs, err := pc.GetPresubmits(orgRepo, baseSHAGetter, headSHAGetters...)
	if err != nil {
		return nil, err
	}
	ret := make([]interface{}, len(jobs))
	for i, v := range jobs {
		ret[i] = v
	}
	return ret, nil
}

func (jf *postsubmitFetchers) getJobsFromInRepoConfig(cfg *config.Config, pc *config.InRepoConfigCacheHandler, orgRepo string, baseSHAGetter config.RefGetter, headSHAGetters ...config.RefGetter) ([]interface{}, error) {
	jobs, err := pc.GetPostsubmits(orgRepo, baseSHAGetter)
	if err != nil {
		return nil, err
	}
	ret := make([]interface{}, len(jobs))
	for i, v := range jobs {
		ret[i] = v
	}
	return ret, nil
}

// FetchJobStruct looks at the sea of all possible Prow Job definitions and
// selects The One that matches the details in the request.
func (gw *Gangway) FetchJobStruct(req *CreateJobExecutionRequest) (*JobStruct, error) {
	// We need to now write a single getProwJobSpec "handler" function
	// that handles all 3 job execution types. In the pub/sub code we do this
	// with 3 separate functions with a certain amount of code duplication
	// across them, but we just do it here in one function for simplicity.

	jobName := req.GetJobName()

	// Only used for postsubmits and presubmits.
	gitRefs := req.GetGitRefs()
	baseRepoCommit := gitRefs.GetBase().GetCommit()
	baseRepoRef := gitRefs.GetBase().GetRef()
	baseSHAGetter := func() (string, error) {
		return baseRepoCommit, nil
	}

	// Only used for presubmits.
	var headSHAGetters []func() (string, error)
	refsToMerge := gitRefs.GetRefsToMerge()
	for _, refToMerge := range refsToMerge {
		refToMerge := refToMerge
		headSHAGetters = append(headSHAGetters, func() (string, error) {
			return refToMerge.GetCommit(), nil
		})
	}

	jobStruct := JobStruct{}
	jobStruct.JobExecutionType = req.GetJobExecutionType()

	cfg := gw.ConfigAgent.Config()
	pc := gw.InRepoConfigCacheHandler

	var jobFetchers jobFetchers

	switch jobStruct.JobExecutionType {
	case JobExecutionType_PERIODIC:
		// Search for the correct Periodic job from the possible candidates
		// defined in the central repo.
		for _, candidateJob := range cfg.AllPeriodics() {
			candidateJob := candidateJob
			if candidateJob.Name == req.GetJobName() {
				(*jobStruct.Job) = &candidateJob
				break
			}
		}
		if jobStruct.Job == nil {
			return nil, fmt.Errorf("failed to find associated periodic job %q", req.GetJobName())
		}
	case JobExecutionType_POSTSUBMIT:
		jobFetchers = &postsubmitFetchers{}
	case JobExecutionType_PRESUBMIT:
		jobFetchers = &presubmitFetchers{}
	}

	// Handle presubmits and postsubmits with the same fetching logic.
	switch jobStruct.JobExecutionType {
	case JobExecutionType_POSTSUBMIT:
		fallthrough
	case JobExecutionType_PRESUBMIT:
		org, repo, err := getOrgRepo(gitRefs.GetBase().GetUrl())
		if err != nil {
			return nil, err
		}
		orgRepo := strings.Join([]string{org, repo}, "/")

		var jobs []interface{}

		if gitRefs == nil {
			// The request only wanted a job defined in the static config.
			// Fetching the statically-defined postsubmit jobs requires providing an "orgRepo" filter.
			jobs = jobFetchers.getJobsFromStaticConfig(cfg, orgRepo)
		} else {
			// The request wants to execute a job defined with gitRefs. This job
			// can be defined either statically in a central repo or from
			// inrepoconfig.
			//
			// For example, let's say the job is a presubmit job from the
			// central repo. A presubmit job must have information about which
			// GitHub pull request or Gerrit change we want to run the job
			// against. This pull request or change number is obviously not
			// found in the central config (it is discovered only at runtime).
			// The gitRefs can be used to look up this information.
			//
			// For the case of a presubmit job defined from inrepoconfig, this
			// gitRefs field performs two jobs: it tells us which pull request
			// or change to clone (as with the example above), but it also tells
			// us which git repo holds the job information (YAML file).
			//
			// Either way, we will look up jobs from the given gitRefs repo (the
			// else clause below) because we don't know whether the specified
			// job is from inrepoconfig or not. We could let clients tell us
			// which one to look into (getJobsFromStaticConfig() vs
			// getJobsFromInRepoConfig()), but this is asking additional
			// information from the client that the client now has to keep track
			// of.
			if pc == nil {
				return nil, errors.New("There is no inrepoconfig cache, but the request wanted to run a job defined from inrepoconfig")
			} else {
				fetched, err := jobFetchers.getJobsFromInRepoConfig(cfg, pc, orgRepo, baseSHAGetter, headSHAGetters...)
				if err != nil {
					return nil, fmt.Errorf("Failed to get %s job from inrepoconfig", jobStruct.JobExecutionType)
				} else {
					jobs = fetched
				}
			}
		}

		// Search for the correct Postsubmit job.
		for _, candidateJob := range jobs {
			candidateJob := candidateJob
			// Filter out jobs that do not match the branch ("ref").
			if !jobFetchers.couldRun(candidateJob, baseRepoRef) {
				continue
			}
			if jobFetchers.name(candidateJob) == jobName {
				if jobStruct.Job != nil {
					return nil, fmt.Errorf("%s matches multiple prow jobs from orgRepo %q; did you define the same job in the central repo and also as an inrepoconfig job?", jobName, orgRepo)
				}
				jobStruct.Job = &candidateJob
			}
		}

		if jobStruct.Job == nil {
			return nil, fmt.Errorf("failed to find associated %s job %q from orgRepo %q", jobStruct.JobExecutionType, jobName, orgRepo)
		}
	}
	return &jobStruct, nil
}

func getHttpRequestHeaders(ctx context.Context) (error, *metadata.MD) {
	// Retrieve HTTP headers from call. All headers are lower-cased.
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return fmt.Errorf("error retrieving metadata from context"), nil
	}
	return nil, &md
}

// assertRequiredHeaders checks that some required headers exist in the given
// metadata. In particular, for GCP (GKE) Prow installations it must have the
// special headers "x-endpoint-api-consumer-type" and
// "x-endpoint-api-consumer-number". These headers allow us to identify the
// caller's associated GCP Project, which we need in order to filter out only
// those Prow Jobs that this project is allowed to create. Otherwise, any caller
// could trigger any Prow Job, which is far from ideal from a security
// standpoint.
//
// Gangway could be configured with a different cloud vendor and thus have
// differently-named headers.
func assertRequiredHeaders(md *metadata.MD, headers []string) error {
	for _, header := range headers {
		values := md.Get(header)
		if len(values) == 0 {
			return fmt.Errorf("could not find required HTTP header %q", header)
		}
	}
	return nil
}

// enableDecoratedLogger turns on a new logger that captures all known
// (interesting) HTTP headers of a gRPC request. We convert these headers into
// log fields so that the logger can be very precise.
func enableDecoratedLogger(allowedAPIClient *config.AllowedAPIClient, md *metadata.MD) {
	knownHeaders := []string{}
	switch allowedAPIClient.Type {
	case "GCP_PROJECT":
		// These headers were drawn from this example:
		// https://github.com/envoyproxy/envoy/issues/13207 (source code appears
		// to be
		// https://github.com/GoogleCloudPlatform/esp-v2/blob/3828042e5b3f840e17837c1a019f4014276014d8/tests/endpoints/bookstore_grpc/server/server.go).
		// Here's an example of what these headers can look like in practice
		// (whitespace edited for readability):
		//
		//     map[
		//       :authority:[localhost:20785]
		//       accept-encoding:[gzip]
		//       content-type:[application/grpc]
		//       user-agent:[Go-http-client/1.1]
		//       x-endpoint-api-consumer-number:[123456]
		//       x-endpoint-api-consumer-type:[PROJECT]
		//       x-envoy-original-method:[GET]
		//       x-envoy-original-path:[/v1/shelves/200?key=api-key]
		//       x-forwarded-proto:[http]
		//       x-request-id:[44770c9a-ee5f-4e36-944e-198b8d9c5196]
		//       ]
		knownHeaders = []string{
			":authority",
			"user-agent",
			"x-endpoint-api-consumer-number",
			"x-endpoint-api-consumer-type",
			"x-envoy-original-method",
			"x-envoy-original-path",
			"x-forwarded-proto",
			"x-request-id",
		}
	}
	fields := make(map[string]interface{})
	for _, header := range knownHeaders {
		values := md.Get(header)
		// Only use the first value. MD stores multiple values in case other
		// entities attempt to overwrite an existing key (it prevents this by
		// storing values as a list of strings).
		//
		// Prefix the field with "http-header/" so that all of the headers here
		// get displayed neatly together (when the fields are sorted by logrus's
		// own output to the console).
		if len(values) > 0 {
			fields[fmt.Sprintf("http-header/%s", header)] = values[0]
		}
	}
	fields["component"] = version.Name

	logrusutil.Init(&logrusutil.DefaultFieldsFormatter{
		PrintLineNumber: true,
		DefaultFields:   fields,
	})
}

func (req *CreateJobExecutionRequest) Validate() error {
	jobName := req.GetJobName()
	jobExecutionType := req.GetJobExecutionType()
	gitRefs := req.GetGitRefs()

	if len(jobName) == 0 {
		return errors.New("job_name field cannot be empty")
	}

	if jobExecutionType == JobExecutionType_JOB_EXECUTION_TYPE_UNSPECIFIED {
		return fmt.Errorf("unsupported JobExecutionType: %s", jobExecutionType)
	}

	// Periodic jobs are not allowed to be defined with gitRefs. This is because
	// gitRefs can denote inrepoconfig repo information (and periodic jobs are
	// not allowed to be defined via inrepoconfig). See
	// https://github.com/kubernetes/test-infra/issues/21729.
	if jobExecutionType == JobExecutionType_PERIODIC && gitRefs != nil {
		logrus.Error("periodic jobs cannot also have gitRefs")
		return errors.New("periodic jobs cannot also have gitRefs")
	}

	// Check whether the gitRefs looks correct on the surface (this
	// is a cursory check only, but still worth doing).
	if gitRefs != nil {
		base := gitRefs.GetBase()
		if base == nil {
			return errors.New("gitRefs: base repo cannot be nil")
		}

		// Check whether the base repo exists (this is required).
		if err := base.Validate(); err != nil {
			return fmt.Errorf("invalid base repo for gitRefs: %s", err)
		}

		// It could be that the job definition is only defined in a GitHub Pull
		// Request or Gerrit Change. So in order to get that job definition we
		// have to merge in the PR or Change.
		//
		// Technically a PR will always only have a single "ref" or head SHA
		// commit. However the data structure we have here is for a list of
		// refs, because we are leaving it possible to request batch jobs (which
		// merge multiple PRs together) through the API in the future.
		refsToMerge := gitRefs.GetRefsToMerge()
		if refsToMerge != nil {
			if jobExecutionType == JobExecutionType_PRESUBMIT || jobExecutionType == JobExecutionType_POSTSUBMIT {
				if len(refsToMerge) > 1 {
					return fmt.Errorf("cannot have more than 1 refsToMerge for %q", jobExecutionType)
				}
			}

			for _, refToMerge := range refsToMerge {
				if err := refToMerge.Validate(); err != nil {
					return fmt.Errorf("invalid refsToMerge entry: %s", err)
				}
			}
		}
	}

	if jobExecutionType != JobExecutionType_PERIODIC {
		// Non-periodic jobs must have a BaseRepo (default repo to clone)
		// defined.
		if gitRefs == nil {
			return fmt.Errorf("gitRefs must be defined for %q", jobExecutionType)
		}
		if err := gitRefs.ValidateGitReferenceDynamic(); err != nil {
			return fmt.Errorf("gitRefs: failed to validate: %s", err)
		}
	}

	// Finally perform some additional checks on the requested PodSpecOptions.
	podSpecOptions := req.GetPodSpecOptions()
	if podSpecOptions != nil {
		envs := podSpecOptions.GetEnvs()
		for k, v := range envs {
			if len(k) == 0 || len(v) == 0 {
				return fmt.Errorf("invalid environment variable key/value pair: %q, %q", k, v)
			}
		}

		labels := podSpecOptions.GetLabels()
		for k, v := range labels {
			if len(k) == 0 || len(v) == 0 {
				return fmt.Errorf("invalid label key/value pair: %q, %q", k, v)
			}

			if len(k) > 63 {
				return fmt.Errorf("invalid label: exceeds 63 characters: %q", k)
			}
		}

		annotations := podSpecOptions.GetAnnotations()
		for k, v := range annotations {
			if len(k) == 0 || len(v) == 0 {
				return fmt.Errorf("invalid annotation key/value pair: %q, %q", k, v)
			}
		}
	}

	return nil
}

func (grs *GitReferenceStatic) Validate() error {
	url := grs.GetUrl()
	if len(url) == 0 {
		return errors.New("url cannot be empty")
	}
	commit := grs.GetCommit()
	if len(commit) == 0 {
		return errors.New("commit cannot be empty")
	}
	ref := grs.GetRef()
	if len(ref) == 0 {
		return errors.New("ref cannot be empty")
	}

	// URL must start with a http or https protocol prefix.
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("url does not start with http[s]://: %q", url)
	}

	// Commit SHA must be a 40-character hex string.
	var validSha = regexp.MustCompile(`^[0-9a-f]{40}$`)
	if !validSha.MatchString(commit) {
		return fmt.Errorf("invalid commit SHA: %q", commit)
	}

	// Git reference names have special restrictions, but we don't bother doing
	// the check here because it's too complicated:
	// https://git-scm.com/docs/git-check-ref-format.
	//
	// (Skip additional checks for `ref`.)

	return nil
}

func (grd *GitReferenceDynamic) ValidateGitReferenceDynamic() error {
	base := grd.GetBase()
	if base == nil {
		return errors.New("baseRepo: base repo cannot be nil")
	}
	if err := base.Validate(); err != nil {
		return fmt.Errorf("invalid base repo for baseRepo: %s", err)
	}

	refsToMerge := grd.GetRefsToMerge()
	for _, refToMerge := range refsToMerge {
		if err := refToMerge.Validate(); err != nil {
			return fmt.Errorf("invalid refsToMerge entry: %s", err)
		}
	}

	return nil
}
