package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/sessionkey"
)

func newWorkspaceSessionTestReconciler(t *testing.T, objs ...client.Object) (*WorkspaceSessionReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := storagev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add storagev1 scheme: %v", err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&sympoziumv1alpha1.WorkspaceSession{}, &sympoziumv1alpha1.AgentRun{}).
		Build()
	return &WorkspaceSessionReconciler{
		Client: cl,
		Scheme: scheme,
		Log:    logr.Discard(),
	}, cl
}

func ptrBool(b bool) *bool { return &b }

// --- name & hash helpers ---------------------------------------------------

func TestWorkspaceNaming_IsDeterministicAndUsesHash(t *testing.T) {
	const agent = "alice"
	const sess = "slack:T123:C456:thread-789"

	hash := sessionkey.Hash(sess)
	if len(hash) != 16 {
		t.Fatalf("expected 16-char hash, got %d", len(hash))
	}

	wsName := workspaceSessionName(agent, sess)
	if !strings.Contains(wsName, hash) {
		t.Errorf("ws name %q should embed hash %q", wsName, hash)
	}
	if strings.Contains(wsName, sess) {
		t.Errorf("ws name %q must NOT embed raw session key", wsName)
	}

	pvc := workspacePVCName(agent, sess, 1)
	if !strings.HasSuffix(pvc, "-g1") {
		t.Errorf("pvc name %q should end with generation suffix", pvc)
	}
	if pvc != workspacePVCName(agent, sess, 1) {
		t.Errorf("pvc name must be deterministic across calls")
	}
	if workspacePVCName(agent, sess, 1) == workspacePVCName(agent, sess, 2) {
		t.Errorf("pvc name must vary across generations")
	}
}

// --- ensureWorkspaceSession ------------------------------------------------

func TestEnsureWorkspaceSession_CreatesAndResyncsMutableFields(t *testing.T) {
	ttl30 := metav1.Duration{Duration: 30 * 24 * time.Hour}
	ttl7 := metav1.Duration{Duration: 7 * 24 * time.Hour}

	agent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "ns1"},
		Spec: sympoziumv1alpha1.AgentSpec{
			Workspace: &sympoziumv1alpha1.WorkspaceSpec{
				PerSessionPVC:    true,
				Size:             "2Gi",
				StorageClassName: "fast",
				IdleTTL:          &ttl30,
			},
		},
	}

	_, cl := newWorkspaceSessionTestReconciler(t, agent)
	ctx := context.Background()
	scheme := cl.Scheme()

	// First call → creates the WorkspaceSession with the Agent's values.
	pvcName1, wsName1, err := ensureWorkspaceSession(ctx, cl, scheme, agent, "sess-A")
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if pvcName1 == "" || wsName1 == "" {
		t.Fatalf("expected non-empty names, got pvc=%q ws=%q", pvcName1, wsName1)
	}

	ws := &sympoziumv1alpha1.WorkspaceSession{}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: wsName1}, ws); err != nil {
		t.Fatalf("get ws: %v", err)
	}
	if ws.Spec.Size != "2Gi" {
		t.Errorf("size: want 2Gi, got %q", ws.Spec.Size)
	}
	if ws.Spec.StorageClassName != "fast" {
		t.Errorf("storage class: want fast, got %q", ws.Spec.StorageClassName)
	}
	if ws.Spec.IdleTTL == nil || ws.Spec.IdleTTL.Duration != ttl30.Duration {
		t.Errorf("idleTTL: want 30d, got %v", ws.Spec.IdleTTL)
	}
	if len(ws.OwnerReferences) != 1 || ws.OwnerReferences[0].UID != agent.UID || ws.OwnerReferences[0].Name != agent.Name {
		t.Errorf("expected ownerRef to agent, got %+v", ws.OwnerReferences)
	}

	// Operator shrinks TTL and bumps size on the Agent.
	agent.Spec.Workspace.Size = "5Gi"
	agent.Spec.Workspace.IdleTTL = &ttl7
	if err := cl.Update(ctx, agent); err != nil {
		t.Fatalf("update agent: %v", err)
	}

	// Second call → re-syncs Size + IdleTTL onto the existing WorkspaceSession.
	pvcName2, wsName2, err := ensureWorkspaceSession(ctx, cl, scheme, agent, "sess-A")
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if wsName2 != wsName1 {
		t.Errorf("ws name must be stable: got %q then %q", wsName1, wsName2)
	}
	if pvcName2 != pvcName1 {
		t.Errorf("pvc name must be stable while generation is unchanged: got %q then %q", pvcName1, pvcName2)
	}

	if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: wsName1}, ws); err != nil {
		t.Fatalf("re-get ws: %v", err)
	}
	if ws.Spec.Size != "5Gi" {
		t.Errorf("size re-sync: want 5Gi, got %q", ws.Spec.Size)
	}
	if ws.Spec.IdleTTL == nil || ws.Spec.IdleTTL.Duration != ttl7.Duration {
		t.Errorf("idleTTL re-sync: want 7d, got %v", ws.Spec.IdleTTL)
	}
	// StorageClassName must NOT be re-synced (would require new PVC).
	if ws.Spec.StorageClassName != "fast" {
		t.Errorf("storage class must remain pinned at creation, got %q", ws.Spec.StorageClassName)
	}
}

// --- agent / agentRun qualification ----------------------------------------

func TestAgentRunQualifiesForSessionPVC(t *testing.T) {
	agentOn := &sympoziumv1alpha1.Agent{
		Spec: sympoziumv1alpha1.AgentSpec{
			Workspace: &sympoziumv1alpha1.WorkspaceSpec{PerSessionPVC: true},
		},
	}
	agentOff := &sympoziumv1alpha1.Agent{
		Spec: sympoziumv1alpha1.AgentSpec{
			Workspace: &sympoziumv1alpha1.WorkspaceSpec{PerSessionPVC: false},
		},
	}
	agentNil := &sympoziumv1alpha1.Agent{}

	parentRef := &sympoziumv1alpha1.ParentRunRef{RunName: "parent-run", SessionKey: "sess-A"}

	cases := []struct {
		name    string
		agent   *sympoziumv1alpha1.Agent
		runSpec sympoziumv1alpha1.AgentRunSpec
		want    bool
	}{
		{"agent opts in + session key + top-level", agentOn, sympoziumv1alpha1.AgentRunSpec{SessionKey: "s1"}, true},
		{"agent opts out", agentOff, sympoziumv1alpha1.AgentRunSpec{SessionKey: "s1"}, false},
		{"agent has no workspace", agentNil, sympoziumv1alpha1.AgentRunSpec{SessionKey: "s1"}, false},
		{"empty session key", agentOn, sympoziumv1alpha1.AgentRunSpec{SessionKey: ""}, false},
		{"sub-agent (parent set) is excluded", agentOn, sympoziumv1alpha1.AgentRunSpec{SessionKey: "s1", Parent: parentRef}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := &sympoziumv1alpha1.AgentRun{Spec: tc.runSpec}
			got := agentRunQualifiesForSessionPVC(run, tc.agent)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// --- listBlockingSessionPeers ----------------------------------------------

func TestListBlockingSessionPeers_FiltersTerminalAndSelf(t *testing.T) {
	hash := sessionkey.Hash("sess-X")
	mkRun := func(name string, phase sympoziumv1alpha1.AgentRunPhase) *sympoziumv1alpha1.AgentRun {
		return &sympoziumv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "ns1",
				Labels: map[string]string{
					"sympozium.ai/instance": "alice",
					SessionKeyHashLabel:     hash,
				},
			},
			Status: sympoziumv1alpha1.AgentRunStatus{Phase: phase},
		}
	}
	// Unrelated runs (different agent / different session) must be ignored.
	otherAgent := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bob-run",
			Namespace: "ns1",
			Labels: map[string]string{
				"sympozium.ai/instance": "bob",
				SessionKeyHashLabel:     hash,
			},
		},
		Status: sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhaseRunning},
	}
	otherSession := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alice-other-sess",
			Namespace: "ns1",
			Labels: map[string]string{
				"sympozium.ai/instance": "alice",
				SessionKeyHashLabel:     sessionkey.Hash("sess-Y"),
			},
		},
		Status: sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhaseRunning},
	}

	self := mkRun("alice-self", sympoziumv1alpha1.AgentRunPhasePending)
	running := mkRun("alice-running", sympoziumv1alpha1.AgentRunPhaseRunning)
	serving := mkRun("alice-serving", sympoziumv1alpha1.AgentRunPhaseServing)
	succeeded := mkRun("alice-done", sympoziumv1alpha1.AgentRunPhaseSucceeded)
	failed := mkRun("alice-failed", sympoziumv1alpha1.AgentRunPhaseFailed)

	_, cl := newWorkspaceSessionTestReconciler(t, self, running, serving, succeeded, failed, otherAgent, otherSession)
	peers, err := listBlockingSessionPeers(context.Background(), cl, self, "alice", hash)
	if err != nil {
		t.Fatalf("list peers: %v", err)
	}

	got := map[string]bool{}
	for _, p := range peers {
		got[p.Name] = true
	}
	want := map[string]bool{"alice-running": true, "alice-serving": true}
	if len(got) != len(want) {
		t.Errorf("peer count: want %d, got %d (%v)", len(want), len(got), got)
	}
	for name := range want {
		if !got[name] {
			t.Errorf("expected peer %q in result", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("unexpected peer %q in result (terminal/self/unrelated should be filtered)", name)
		}
	}
}

func TestPeerBlocksAdmission_FIFOOrdering(t *testing.T) {
	t0 := metav1.NewTime(time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC))
	t1 := metav1.NewTime(t0.Add(time.Minute))
	mkRun := func(name string, created metav1.Time, status sympoziumv1alpha1.AgentRunStatus) *sympoziumv1alpha1.AgentRun {
		return &sympoziumv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns1", CreationTimestamp: created},
			Status:     status,
		}
	}
	pending := func(name string, created metav1.Time) *sympoziumv1alpha1.AgentRun {
		return mkRun(name, created, sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhasePending})
	}
	started := metav1.Now()

	cases := []struct {
		name string
		self *sympoziumv1alpha1.AgentRun
		peer *sympoziumv1alpha1.AgentRun
		want bool
	}{
		{
			"running peer always blocks",
			pending("self", t0),
			mkRun("peer", t1, sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhaseRunning}),
			true,
		},
		{
			"serving peer always blocks",
			pending("self", t0),
			mkRun("peer", t1, sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhaseServing}),
			true,
		},
		{
			"pending peer with a Job holds the lock",
			pending("self", t0),
			mkRun("peer", t1, sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhasePending, JobName: "peer-job"}),
			true,
		},
		{
			"pending peer with StartedAt holds the lock",
			pending("self", t0),
			mkRun("peer", t1, sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhasePending, StartedAt: &started}),
			true,
		},
		{
			"older waiter blocks a younger one",
			pending("self", t1),
			pending("peer", t0),
			true,
		},
		{
			"younger waiter does NOT block an older one",
			pending("self", t0),
			pending("peer", t1),
			false,
		},
		{
			"equal timestamps tie-break by name: lexically smaller peer blocks",
			pending("z-self", t0),
			pending("a-peer", t0),
			true,
		},
		{
			"equal timestamps tie-break by name: lexically larger peer does not block",
			pending("a-self", t0),
			pending("z-peer", t0),
			false,
		},
		{
			"terminal peer never blocks",
			pending("self", t1),
			mkRun("peer", t0, sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhaseSucceeded}),
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := peerBlocksAdmission(tc.self, tc.peer); got != tc.want {
				t.Errorf("peerBlocksAdmission() = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- reconciler: PVC create + status -------------------------------------

func TestWorkspaceSessionReconcile_CreatesPVCAndSetsBound(t *testing.T) {
	agent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "ns1", UID: "agent-uid"},
		Spec:       sympoziumv1alpha1.AgentSpec{Workspace: &sympoziumv1alpha1.WorkspaceSpec{PerSessionPVC: true}},
	}
	wsName := workspaceSessionName(agent.Name, "sess-A")
	ws := &sympoziumv1alpha1.WorkspaceSession{
		ObjectMeta: metav1.ObjectMeta{
			Name: wsName, Namespace: "ns1",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "sympozium.ai/v1alpha1", Kind: "Agent",
				Name: agent.Name, UID: agent.UID, Controller: ptrBool(true),
			}},
		},
		Spec: sympoziumv1alpha1.WorkspaceSessionSpec{
			AgentRef: "alice", SessionKey: "sess-A", Size: "1Gi",
		},
	}

	r, cl := newWorkspaceSessionTestReconciler(t, agent, ws)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "ns1", Name: wsName},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &sympoziumv1alpha1.WorkspaceSession{}
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "ns1", Name: wsName}, got); err != nil {
		t.Fatalf("re-get ws: %v", err)
	}
	if got.Status.Phase != sympoziumv1alpha1.WorkspaceSessionPhaseBound {
		t.Errorf("phase: want Bound, got %q", got.Status.Phase)
	}
	if got.Status.PVCName == "" {
		t.Errorf("status.pvcName must be set after reconcile")
	}

	pvc := &corev1.PersistentVolumeClaim{}
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "ns1", Name: got.Status.PVCName}, pvc); err != nil {
		t.Fatalf("get pvc: %v", err)
	}
	if pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("expected RWO PVC, got %v", pvc.Spec.AccessModes)
	}
	want := resource.MustParse("1Gi")
	got1 := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if got1.Cmp(want) != 0 {
		t.Errorf("size: want 1Gi, got %s", got1.String())
	}
}

// --- reconciler: PVC resize ----------------------------------------------

func TestReconcilePVCResize_GrowsWhenStorageClassAllowsExpansion(t *testing.T) {
	sc := &storagev1.StorageClass{
		ObjectMeta:           metav1.ObjectMeta{Name: "fast"},
		AllowVolumeExpansion: ptrBool(true),
		Provisioner:          "test",
	}
	scName := "fast"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-1", Namespace: "ns1"},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &scName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}
	ws := &sympoziumv1alpha1.WorkspaceSession{
		ObjectMeta: metav1.ObjectMeta{Name: "ws", Namespace: "ns1"},
		Spec:       sympoziumv1alpha1.WorkspaceSessionSpec{Size: "5Gi"},
	}

	r, cl := newWorkspaceSessionTestReconciler(t, sc, pvc)
	cond, err := r.reconcilePVCResize(context.Background(), logr.Discard(), ws, pvc, resource.MustParse("5Gi"))
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "Expanded" {
		t.Errorf("expected Expanded condition (False), got %+v", cond)
	}

	got := &corev1.PersistentVolumeClaim{}
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "ns1", Name: "pvc-1"}, got); err != nil {
		t.Fatalf("re-get pvc: %v", err)
	}
	want := resource.MustParse("5Gi")
	cur := got.Spec.Resources.Requests[corev1.ResourceStorage]
	if cur.Cmp(want) != 0 {
		t.Errorf("pvc not patched: want 5Gi, got %s", cur.String())
	}
}

func TestReconcilePVCResize_RefusesShrink(t *testing.T) {
	scName := "fast"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-1", Namespace: "ns1"},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &scName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("5Gi")},
			},
		},
	}
	ws := &sympoziumv1alpha1.WorkspaceSession{
		ObjectMeta: metav1.ObjectMeta{Name: "ws", Namespace: "ns1"},
		Spec:       sympoziumv1alpha1.WorkspaceSessionSpec{Size: "1Gi"},
	}

	r, cl := newWorkspaceSessionTestReconciler(t, pvc)
	cond, err := r.reconcilePVCResize(context.Background(), logr.Discard(), ws, pvc, resource.MustParse("1Gi"))
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "ShrinkUnsupported" {
		t.Errorf("expected ResizeBlocked/ShrinkUnsupported, got %+v", cond)
	}

	got := &corev1.PersistentVolumeClaim{}
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "ns1", Name: "pvc-1"}, got); err != nil {
		t.Fatalf("re-get pvc: %v", err)
	}
	want := resource.MustParse("5Gi")
	cur := got.Spec.Resources.Requests[corev1.ResourceStorage]
	if cur.Cmp(want) != 0 {
		t.Errorf("pvc must be left intact on shrink: want 5Gi, got %s", cur.String())
	}
}

func TestReconcilePVCResize_BlocksGrowWhenClassNotExpandable(t *testing.T) {
	sc := &storagev1.StorageClass{
		ObjectMeta:           metav1.ObjectMeta{Name: "slow"},
		AllowVolumeExpansion: ptrBool(false),
		Provisioner:          "test",
	}
	scName := "slow"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-1", Namespace: "ns1"},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &scName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}
	ws := &sympoziumv1alpha1.WorkspaceSession{
		ObjectMeta: metav1.ObjectMeta{Name: "ws", Namespace: "ns1"},
		Spec:       sympoziumv1alpha1.WorkspaceSessionSpec{Size: "5Gi"},
	}

	r, cl := newWorkspaceSessionTestReconciler(t, sc, pvc)
	cond, err := r.reconcilePVCResize(context.Background(), logr.Discard(), ws, pvc, resource.MustParse("5Gi"))
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "ExpansionNotAllowed" {
		t.Errorf("expected ResizeBlocked/ExpansionNotAllowed, got %+v", cond)
	}
	got := &corev1.PersistentVolumeClaim{}
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "ns1", Name: "pvc-1"}, got); err != nil {
		t.Fatalf("re-get pvc: %v", err)
	}
	want := resource.MustParse("1Gi")
	cur := got.Spec.Resources.Requests[corev1.ResourceStorage]
	if cur.Cmp(want) != 0 {
		t.Errorf("pvc must be left intact when class blocks expansion: want 1Gi, got %s", cur.String())
	}
}

func TestReconcilePVCResize_ClearsStaleBlockedConditionWhenInSync(t *testing.T) {
	scName := "fast"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-1", Namespace: "ns1"},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &scName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
			},
		},
	}
	ws := &sympoziumv1alpha1.WorkspaceSession{
		ObjectMeta: metav1.ObjectMeta{Name: "ws", Namespace: "ns1"},
		Spec:       sympoziumv1alpha1.WorkspaceSessionSpec{Size: "2Gi"},
		Status: sympoziumv1alpha1.WorkspaceSessionStatus{
			Conditions: []metav1.Condition{{
				Type: "ResizeBlocked", Status: metav1.ConditionTrue, Reason: "ShrinkUnsupported",
			}},
		},
	}

	r, _ := newWorkspaceSessionTestReconciler(t, pvc)
	cond, err := r.reconcilePVCResize(context.Background(), logr.Discard(), ws, pvc, resource.MustParse("2Gi"))
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "InSync" {
		t.Errorf("expected ResizeBlocked cleared (False/InSync), got %+v", cond)
	}
}

// --- helpers: isTerminalPhase + condition --------------------------------

func TestIsTerminalPhase(t *testing.T) {
	cases := map[sympoziumv1alpha1.AgentRunPhase]bool{
		sympoziumv1alpha1.AgentRunPhasePending:          false,
		sympoziumv1alpha1.AgentRunPhaseRunning:          false,
		sympoziumv1alpha1.AgentRunPhaseServing:          false,
		sympoziumv1alpha1.AgentRunPhasePostRunning:      false,
		sympoziumv1alpha1.AgentRunPhaseAwaitingDelegate: false,
		sympoziumv1alpha1.AgentRunPhaseSucceeded:        true,
		sympoziumv1alpha1.AgentRunPhaseFailed:           true,
	}
	for p, want := range cases {
		if got := isTerminalPhase(p); got != want {
			t.Errorf("phase %q: want terminal=%v, got %v", p, want, got)
		}
	}
}

func TestSetCondition_UpsertsAndPreservesLastTransitionTime(t *testing.T) {
	earlier := metav1.NewTime(time.Now().Add(-time.Hour))
	conds := []metav1.Condition{{
		Type: "Ready", Status: metav1.ConditionTrue, Reason: "Bound",
		LastTransitionTime: earlier,
	}}

	// Same status → LastTransitionTime preserved.
	out := setCondition(conds, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue, Reason: "StillBound",
		LastTransitionTime: metav1.Now(),
	})
	if len(out) != 1 {
		t.Fatalf("want 1 cond, got %d", len(out))
	}
	if !out[0].LastTransitionTime.Equal(&earlier) {
		t.Errorf("LastTransitionTime must be preserved on no-status-change; got %v", out[0].LastTransitionTime)
	}
	if out[0].Reason != "StillBound" {
		t.Errorf("Reason must be updated; got %q", out[0].Reason)
	}

	// Status flip → LastTransitionTime advances.
	now := metav1.Now()
	out = setCondition(out, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: "Lost",
		LastTransitionTime: now,
	})
	if out[0].LastTransitionTime.Equal(&earlier) {
		t.Errorf("LastTransitionTime must advance on status flip")
	}
	if out[0].Status != metav1.ConditionFalse || out[0].Reason != "Lost" {
		t.Errorf("status/reason not updated: %+v", out[0])
	}

	// New type → appended.
	out = setCondition(out, metav1.Condition{
		Type: "Other", Status: metav1.ConditionTrue, Reason: "X",
	})
	if len(out) != 2 {
		t.Errorf("expected 2 conditions, got %d", len(out))
	}
}
