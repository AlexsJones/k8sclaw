// In-place gate retry: the gate rejects attempt 1, the pod stays alive and
// continues on the same conversation, and the gate approves attempt 2.
//
// What this proves that the successor-clone specs do not: the whole chain runs
// on ONE AgentRun. Attempt 2 is not a new CR, the run reports AwaitingGate
// while it waits, and status.attempts[] carries a per-attempt timeline the UI
// renders.

const INSTANCE = `cy-gate-inplace-${Date.now()}`;
let RUN_NAME = "";

function applyInstance() {
  // The gate reads the run's own status.attempt and retries once. A hook that
  // always retried would loop until maxAttempts and resolve as
  // retries-exhausted, which is a different assertion.
  const manifest = `apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: ${INSTANCE}
  namespace: default
spec:
  authRefs:
    - provider: lm-studio
      secret: ""
  agents:
    default:
      model: ${Cypress.env("TEST_MODEL")}
      baseURL: http://host.docker.internal:1234/v1
      lifecycle:
        gateDefault: block
        retry:
          maxAttempts: 3
          on: ["gate"]
        rbac:
          - apiGroups: ["sympozium.ai"]
            resources: ["agentruns"]
            verbs: ["get", "patch"]
        postRun:
          - name: inplace-gate
            image: soldevelo/kubectl:1.36
            gate: true
            timeout: 5m
            command: ["sh", "-c"]
            args:
              - |
                ATTEMPT=$(kubectl get agentrun $AGENT_RUN_ID -n $AGENT_NAMESPACE -o jsonpath='{.status.attempt}')
                [ -z "$ATTEMPT" ] && ATTEMPT=1
                if [ "$ATTEMPT" -lt 2 ]; then
                  VERDICT='{\\"action\\":\\"retry\\",\\"reason\\":\\"cypress wants a second attempt\\",\\"response\\":\\"GATE_SAYS_TRY_AGAIN\\"}'
                else
                  VERDICT='{\\"action\\":\\"approve\\",\\"reason\\":\\"cypress-inplace\\"}'
                fi
                kubectl patch agentrun $AGENT_RUN_ID -n $AGENT_NAMESPACE --type=merge \\
                  -p "{\\"metadata\\":{\\"annotations\\":{\\"sympozium.ai/gate-verdict\\":\\"$VERDICT\\"}}}"
`;
  cy.writeFile(`cypress/tmp/${INSTANCE}.yaml`, manifest);
  cy.exec(`kubectl apply -f cypress/tmp/${INSTANCE}.yaml`);
}

function authHeaders(): Record<string, string> {
  const token = Cypress.env("API_TOKEN");
  const h: Record<string, string> = { "Content-Type": "application/json" };
  if (token) h["Authorization"] = `Bearer ${token}`;
  return h;
}

function getRun(runName: string) {
  return cy.request({
    url: `/api/v1/runs/${runName}?namespace=default`,
    headers: authHeaders(),
    failOnStatusCode: false,
  });
}

/** Poll until the run reports AwaitingGate at least once. That phase is the
 *  observable proof the pod parked rather than exiting. */
function waitForAwaitingGate(runName: string, timeoutMs = 5 * 60 * 1000) {
  const started = Date.now();
  const poll = (): Cypress.Chainable<void> => {
    return getRun(runName).then((resp) => {
      const phase = resp.body?.status?.phase as string | undefined;
      if (phase === "AwaitingGate") return;
      if (phase === "Succeeded" || phase === "Failed") {
        throw new Error(`run reached ${phase} without ever parking`);
      }
      if (Date.now() - started > timeoutMs) {
        throw new Error(`waitForAwaitingGate timed out; last phase=${phase}`);
      }
      cy.wait(2000, { log: false });
      return poll();
    });
  };
  return poll();
}

describe("Response gate -- in-place retry", () => {
  before(() => {
    applyInstance();
    cy.wait(3000);
    cy.dispatchRun(INSTANCE, "Reply with exactly: GATE_RESUME_SENTINEL").then(
      (name) => {
        RUN_NAME = name;
      },
    );
    cy.then(() => waitForAwaitingGate(RUN_NAME));
  });

  after(() => {
    if (RUN_NAME) cy.deleteRun(RUN_NAME);
    cy.deleteAgent(INSTANCE);
  });

  it("renders AwaitingGate with an attempt timeline while parked", () => {
    cy.visit(`/runs/${RUN_NAME}`);
    cy.get("[data-testid='gate-approval-bar']", { timeout: 30000 }).should(
      "be.visible",
    );
    cy.get("[data-testid='attempt-timeline']", { timeout: 30000 })
      .should("be.visible")
      .and("contain.text", "Attempt 1");
  });

  it("continues on the same run and approves the second attempt", () => {
    cy.then(() => cy.waitForRunTerminal(RUN_NAME, 5 * 60 * 1000)).then(
      (phase) => {
        expect(phase).to.eq("Succeeded");
      },
    );

    cy.then(() =>
      getRun(RUN_NAME).then((resp) => {
        expect(resp.body.status.gateVerdict).to.eq("approved");
        expect(resp.body.status.attempt).to.eq(2);

        // The whole chain lives on this one CR.
        const attempts = resp.body.status.attempts as Array<{
          attempt: number;
          gateVerdict?: string;
        }>;
        expect(attempts, "status.attempts").to.have.length.of.at.least(2);
        expect(attempts[0].gateVerdict).to.eq("retried");
        expect(attempts[1].gateVerdict).to.eq("approved");
      }),
    );

    // No successor CR: that is the difference from clone-based retry.
    cy.exec(
      `kubectl get agentruns -n default -l sympozium.ai/retry-of=${RUN_NAME} -o name`,
      { failOnNonZeroExit: false },
    ).then((res) => {
      expect(res.stdout.trim(), "successor runs").to.eq("");
    });
  });

  it("hides the approve/reject controls once the verdict is consumed", () => {
    cy.visit(`/runs/${RUN_NAME}`);
    cy.contains("Succeeded", { timeout: 30000 }).should("be.visible");
    cy.get("[data-testid='gate-approval-bar']").should("not.exist");
    cy.get("[data-testid='attempt-timeline']")
      .should("be.visible")
      .and("contain.text", "Attempt 2");
  });
});

export {};
