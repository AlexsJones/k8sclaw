// Test: a custom OpenAI-compatible endpoint may be unauthenticated.

describe("Create Agent — custom endpoint without authentication", () => {
  it("allows the Auth step to be skipped", () => {
    cy.visit("/agents");

    cy.contains("button", "Create Agent", { timeout: 20000 }).click();

    cy.get("[role='dialog']")
      .find("input[placeholder='my-agent']")
      .clear()
      .type(`cypress-custom-no-auth-${Date.now()}`);
    cy.wizardNext();

    cy.get("[role='dialog']")
      .find("button[role='combobox']")
      .click({ force: true });
    cy.get("[data-radix-popper-content-wrapper]")
      .contains("Custom")
      .click({ force: true });
    cy.wizardNext();

    cy.get("[role='dialog']").contains("API Key");
    cy.get("[role='dialog']")
      .contains("button", "Next")
      .should("not.be.disabled")
      .click();

    cy.get("[role='dialog']").find("input[placeholder='gpt-4o']");
  });
});
