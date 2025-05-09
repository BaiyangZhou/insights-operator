package conditional

import (
	"context"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"

	"github.com/openshift/insights-operator/pkg/record"
	"github.com/openshift/insights-operator/pkg/types"
	"github.com/openshift/insights-operator/pkg/utils/marshal"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func TestGroupRawLogRequestsByNamespace(t *testing.T) {
	tests := []struct {
		name           string
		rawLogReuests  []RawLogRequest
		expectedResult map[string]LogRequest
	}{
		{
			name:           "nil input returns empty map",
			rawLogReuests:  nil,
			expectedResult: map[string]LogRequest{},
		},
		{
			name: "unique log requests are reflected in the map",
			rawLogReuests: []RawLogRequest{
				{
					Namespace:    "namespace-A",
					PodNameRegex: "test-A-.*",
					Previous:     true,
					Messages: []string{
						"message 1.*",
						"message 2.*",
					},
				},
				{
					Namespace:    "namespace-B",
					PodNameRegex: "test-B-.*",
					Previous:     false,
					Messages: []string{
						"message 1.*",
						"message 2.*",
					},
				},
			},
			expectedResult: map[string]LogRequest{
				"namespace-A": {
					Namespace: "namespace-A",
					PodNameRegexToMessages: map[PodNameRegexPrevious]sets.Set[string]{
						{"test-A-.*", true}: sets.Set[string](sets.NewString("message 1.*", "message 2.*")),
					},
				},
				"namespace-B": {
					Namespace: "namespace-B",
					PodNameRegexToMessages: map[PodNameRegexPrevious]sets.Set[string]{
						{"test-B-.*", false}: sets.Set[string](sets.NewString("message 1.*", "message 2.*")),
					},
				},
			},
		},
		{
			name: "same pod name regexes and previous values are joined",
			rawLogReuests: []RawLogRequest{
				{
					Namespace:    "namespace-A",
					PodNameRegex: "test-A-.*",
					Previous:     true,
					Messages: []string{
						"message 1.*",
						"message 2.*",
					},
				},
				{
					Namespace:    "namespace-A",
					PodNameRegex: "test-B-.*",
					Previous:     false,
					Messages: []string{
						"message 1.*",
						"message 2.*",
						"message 3.*",
						"message 4.*",
					},
				},
				{
					Namespace:    "namespace-A",
					PodNameRegex: "test-B-.*",
					Previous:     false,
					Messages: []string{
						"message 5.*",
					},
				},
			},
			expectedResult: map[string]LogRequest{
				"namespace-A": {
					Namespace: "namespace-A",
					PodNameRegexToMessages: map[PodNameRegexPrevious]sets.Set[string]{
						{"test-A-.*", true}:  sets.Set[string](sets.NewString("message 1.*", "message 2.*")),
						{"test-B-.*", false}: sets.Set[string](sets.NewString("message 1.*", "message 2.*", "message 3.*", "message 4.*", "message 5.*")),
					},
				},
			},
		},
		{
			name: "same Pod regex in the same namespace but different container run",
			rawLogReuests: []RawLogRequest{
				{
					Namespace:    "namespace-A",
					PodNameRegex: "test-A-.*",
					Previous:     true,
					Messages: []string{
						"message 1.*",
						"message 2.*",
					},
				},
				{
					Namespace:    "namespace-A",
					PodNameRegex: "test-A-.*",
					Previous:     false,
					Messages: []string{
						"message 3.*",
						"message 4.*",
					},
				},
			},
			expectedResult: map[string]LogRequest{
				"namespace-A": {
					Namespace: "namespace-A",
					PodNameRegexToMessages: map[PodNameRegexPrevious]sets.Set[string]{
						{"test-A-.*", true}:  sets.Set[string](sets.NewString("message 1.*", "message 2.*")),
						{"test-A-.*", false}: sets.Set[string](sets.NewString("message 3.*", "message 4.*")),
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resultMap := groupRawLogRequestsByNamespace(tt.rawLogReuests)
			assert.Equal(t, tt.expectedResult, resultMap)
		})
	}
}

func TestListOfMessagesToRegex(t *testing.T) {
	tests := []struct {
		name                string
		inputMessages       sets.Set[string]
		expectedStringRegex []string
		expectedError       error
	}{
		{
			name:                "empty Set as input",
			inputMessages:       sets.Set[string]{},
			expectedStringRegex: []string{},
			expectedError:       fmt.Errorf("input messages are nil or empty"),
		},
		{
			name:                "nil input",
			inputMessages:       nil,
			expectedStringRegex: []string{},
			expectedError:       fmt.Errorf("input messages are nil or empty"),
		},
		{
			name:                "single message as input",
			inputMessages:       sets.Set[string](sets.NewString("wal:\\ max\\ entry\\ size\\ limit\\ exceeded")),
			expectedStringRegex: []string{"wal:\\ max\\ entry\\ size\\ limit\\ exceeded"},
			expectedError:       nil,
		},
		{
			name: "multiple messages as input",
			inputMessages: sets.Set[string](sets.NewString(
				"Foo",
				"Bar",
				"first",
				"second",
			)),
			expectedStringRegex: []string{"Bar", "Foo", "first", "second"},
			expectedError:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testRegex, err := listOfMessagesToRegex(tt.inputMessages)
			assert.Equal(t, tt.expectedError, err)
			if testRegex != nil {
				// order of the message is not guaranteed
				messageExp := strings.Split(testRegex.String(), "|")
				assert.ElementsMatch(t, tt.expectedStringRegex, messageExp)
			}
		})
	}
}

func TestCreatePodToContainersMap(t *testing.T) {
	tests := []struct {
		name         string
		logRequest   LogRequest
		pods         []*corev1.Pod
		expectedMap  map[string]containersAndMessages
		expectedErrs []error
	}{
		{
			name: "empty Pod list",
			logRequest: LogRequest{
				Namespace: "test-namespace",
				PodNameRegexToMessages: map[PodNameRegexPrevious]sets.Set[string]{
					{"foo-pod.*", false}: sets.Set[string](sets.NewString("foo-message")),
				},
			},
			pods:        []*corev1.Pod{},
			expectedMap: map[string]containersAndMessages{},
		},
		{
			name: "Pod name regex fails to compile",
			logRequest: LogRequest{
				Namespace: "test-namespace",
				PodNameRegexToMessages: map[PodNameRegexPrevious]sets.Set[string]{
					{"(?!BBB)", false}: sets.Set[string](sets.NewString("foo-message")),
					{"?!", false}:      sets.Set[string](sets.NewString("foo-message")),
				},
			},
			pods:        []*corev1.Pod{},
			expectedMap: map[string]containersAndMessages{},
			expectedErrs: []error{
				&syntax.Error{Code: syntax.ErrInvalidPerlOp, Expr: "(?!"},
				&syntax.Error{Code: syntax.ErrMissingRepeatArgument, Expr: "?"},
			},
		},
		{
			name: "pod name regex matching some Pods",
			logRequest: LogRequest{
				Namespace: "test-namespace",
				PodNameRegexToMessages: map[PodNameRegexPrevious]sets.Set[string]{
					{"foo-.*", false}: sets.Set[string](sets.NewString("foo-message")),
				},
			},
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo-pod-1",
						Namespace: "test-namespace",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "test-foo-container-1",
							},
							{
								Name: "test-foo-container-2",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "bar-1",
						Namespace: "test-namespace",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "test-bar-container-1",
							},
							{
								Name: "test-bar-container-2",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "baz-1",
						Namespace: "test-namespace",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "test-baz-container-1",
							},
							{
								Name: "test-baz-container-2",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo-next",
						Namespace: "test-namespace",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "test-foo-next-container-1",
							},
							{
								Name: "test-foo-next-container-2",
							},
						},
					},
				},
			},
			expectedMap: map[string]containersAndMessages{
				"foo-pod-1": {
					containerNames: []string{"test-foo-container-1", "test-foo-container-2"},
					messsages:      sets.Set[string](sets.NewString("foo-message")),
				},
				"foo-next": {
					containerNames: []string{"test-foo-next-container-1", "test-foo-next-container-2"},
					messsages:      sets.Set[string](sets.NewString("foo-message")),
				},
			},
		},
		{
			name: "pods matching multiple pod name regexes",
			logRequest: LogRequest{
				Namespace: "test-namespace",
				PodNameRegexToMessages: map[PodNameRegexPrevious]sets.Set[string]{
					{"foo-.*", false}: sets.Set[string](sets.NewString("foo-general")),
					{"foo-1", false}:  sets.Set[string](sets.NewString(".*hello world.*", ".*bye.*")),
				},
			},
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo-1",
						Namespace: "test-namespace",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "foo-1-container-1",
							},
							{
								Name: "foo-2-container-2",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "bar-1",
						Namespace: "another-namespace",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "test-bar-container-1",
							},
							{
								Name: "test-bar-container-2",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo-bar",
						Namespace: "test-namespace",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "foo-bar-container-1",
							},
							{
								Name: "foo-bar-container-2",
							},
						},
					},
				},
			},

			expectedMap: map[string]containersAndMessages{
				"foo-1": {
					containerNames: []string{"foo-1-container-1", "foo-2-container-2"},
					messsages:      sets.Set[string](sets.NewString("foo-general", ".*hello world.*", ".*bye.*")),
				},
				"foo-bar": {
					containerNames: []string{"foo-bar-container-1", "foo-bar-container-2"},
					messsages:      sets.Set[string](sets.NewString("foo-general")),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli := kubefake.NewSimpleClientset()
			for _, p := range tt.pods {
				err := cli.Tracker().Add(p)
				assert.NoError(t, err)
			}

			mapResult, errs := createPodToContainersAndMessagesMapping(context.Background(), cli.CoreV1(), tt.logRequest)
			if len(tt.expectedErrs) > 0 {
				assert.ElementsMatch(t, tt.expectedErrs, errs)
			}
			assert.EqualValues(t, tt.expectedMap, mapResult)
		})
	}
}

func TestGetAndFilterContainerLogs(t *testing.T) {
	tests := []struct {
		name            string
		containerLogReq ContainerLogRequest
		testingPod      *corev1.Pod
		expectedRecord  *record.Record
		expectedErr     error
	}{
		{
			name: "existing and matching container log message creates expected record",
			containerLogReq: ContainerLogRequest{
				Namespace:     "test-namespace",
				PodName:       "foo",
				ContainerName: "foo-container",
				MessageRegex:  regexp.MustCompile(".*fake logs.*"),
			},
			testingPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "test-namespace",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "foo-container"},
					},
				},
			},
			expectedRecord: &record.Record{
				Name: "namespaces/test-namespace/pods/foo/foo-container/current.log",
				Item: marshal.RawByte("fake logs\n"),
			},
			expectedErr: nil,
		},
		{
			name: "non-matching messages creates nil record",
			containerLogReq: ContainerLogRequest{
				Namespace:     "test-namespace",
				PodName:       "foo",
				ContainerName: "foo-container",
				MessageRegex:  regexp.MustCompile(".*this will not match.*"),
			},
			testingPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "test-namespace",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "foo-container"},
					},
				},
			},
			expectedRecord: nil,
			expectedErr: &types.Warning{
				UnderlyingValue: fmt.Errorf("not found any data for the container foo-container in the Pod foo in the test-namespace namespace"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli := kubefake.NewSimpleClientset()
			ctx := context.Background()
			_, err := cli.CoreV1().Pods("test-namespace").Create(ctx, tt.testingPod, metav1.CreateOptions{})
			assert.NoError(t, err)
			rec, err := getAndFilterContainerLogs(ctx, cli.CoreV1(), tt.containerLogReq)
			assert.Equal(t, tt.expectedRecord, rec)
			assert.Equal(t, tt.expectedErr, err)
		})
	}
}

func TestGatherContainerLogs(t *testing.T) {
	tests := []struct {
		name            string
		rawLogRequests  []RawLogRequest
		testingPods     []corev1.Pod
		expectedRecords []record.Record
		expectedErrors  []error
	}{
		{
			name:            "empty raw log request input",
			rawLogRequests:  []RawLogRequest{},
			testingPods:     nil,
			expectedRecords: nil,
			expectedErrors:  nil,
		},
		{
			name: "log request but no Pods found",
			rawLogRequests: []RawLogRequest{
				{
					Namespace:    "test-namespace",
					PodNameRegex: "test-.*",
					Messages: []string{
						".* foo",
					},
				},
			},
			testingPods:     nil,
			expectedRecords: nil,
			expectedErrors:  nil,
		},
		{
			name: "log request with empty messages",
			rawLogRequests: []RawLogRequest{
				{
					Namespace:    "test-namespace",
					PodNameRegex: "test-.*",
					Messages:     []string{},
				},
			},
			testingPods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-foo",
						Namespace: "test-namespace",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "foo-1",
							},
							{
								Name: "foo-2",
							},
						},
					},
				},
			},
			expectedRecords: nil,
			expectedErrors: []error{
				fmt.Errorf("Failed to compile the list of messages for one of the [test-.*] Pod name regexes for test-namespace namespace: input messages are nil or empty"), //nolint:lll
			},
		},
		{
			name: "Messages regex cannot be compiled",
			rawLogRequests: []RawLogRequest{
				{
					Namespace:    "test-namespace",
					PodNameRegex: "test-.*",
					Messages: []string{
						"\\",
					},
				},
			},
			testingPods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-foo",
						Namespace: "test-namespace",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "foo-1",
							},
							{
								Name: "foo-2",
							},
						},
					},
				},
			},
			expectedRecords: nil,
			expectedErrors: []error{
				fmt.Errorf("Failed to compile the list of messages for one of the [test-.*] Pod name regexes for test-namespace namespace: error parsing regexp: trailing backslash at end of expression: ``"), //nolint:lll
			},
		},
		{
			name: "Pod name regex cannot be compiled",
			rawLogRequests: []RawLogRequest{
				{
					Namespace:    "test-namespace",
					PodNameRegex: "\\",
					Messages: []string{
						"foo",
						"bar",
					},
				},
			},
			testingPods:     nil,
			expectedRecords: nil,
			expectedErrors: []error{
				fmt.Errorf("failed to get matching pod names for namespace test-namespace: error parsing regexp: trailing backslash at end of expression: ``"), //nolint:lll
			},
		},
		{
			name: "two Pods, but only one Pod in testing namespace with two matching container logs",
			rawLogRequests: []RawLogRequest{
				{
					Namespace:    "test-namespace",
					PodNameRegex: "test-*.",
					Messages: []string{
						"fake",
						"logs",
					},
				},
			},
			testingPods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-foo",
						Namespace: "test-namespace",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "foo-1",
							},
							{
								Name: "foo-2",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-foo",
						Namespace: "another-namespace",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "foo-1",
							},
							{
								Name: "foo-2",
							},
						},
					},
				},
			},
			expectedRecords: []record.Record{
				{
					Name: "namespaces/test-namespace/pods/test-foo/foo-1/current.log",
					Item: marshal.RawByte("fake logs\n"),
				},
				{
					Name: "namespaces/test-namespace/pods/test-foo/foo-2/current.log",
					Item: marshal.RawByte("fake logs\n"),
				},
			},
			expectedErrors: []error{},
		},
		{
			name: "two Pods and two raw log requests",
			rawLogRequests: []RawLogRequest{
				{
					Namespace:    "foo-namespace",
					PodNameRegex: "test-*.",
					Messages: []string{
						"fake",
						"logs",
					},
				},
				{
					Namespace:    "bar-namespace",
					PodNameRegex: "test-*.",
					Messages: []string{
						"fake",
						"logs",
					},
					Previous: true,
				},
			},
			testingPods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-foo",
						Namespace: "foo-namespace",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "foo-1",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-bar",
						Namespace: "bar-namespace",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "bar-1",
							},
						},
					},
				},
			},
			expectedRecords: []record.Record{
				{
					Name: "namespaces/foo-namespace/pods/test-foo/foo-1/current.log",
					Item: marshal.RawByte("fake logs\n"),
				},
				{
					Name: "namespaces/bar-namespace/pods/test-bar/bar-1/previous.log",
					Item: marshal.RawByte("fake logs\n"),
				},
			},
			expectedErrors: []error{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli := kubefake.NewSimpleClientset()
			ctx := context.Background()

			for _, p := range tt.testingPods {
				_, err := cli.CoreV1().Pods(p.Namespace).Create(ctx, &p, metav1.CreateOptions{})
				assert.NoError(t, err)
			}

			recs, errs := gatherContainerLogs(ctx, cli.CoreV1(), tt.rawLogRequests)
			assert.ElementsMatch(t, tt.expectedRecords, recs)
			assert.ElementsMatch(t, tt.expectedErrors, errs)
		})
	}
}
