import { expect, test } from "@playwright/test";
import { installFrontierEventHarness, mockFrontier } from "./frontierMock";

test.beforeEach(async ({ page }) => {
  await installFrontierEventHarness(page);
  await mockFrontier(page);
});

test("Home reads its authoritative dashboard and opens Tasks", async ({ page }) => {
  const snapshots: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/api/v1/views/")) snapshots.push(new URL(request.url()).pathname);
  });

  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Current work, continuity, and delivery evidence" })).toBeVisible();
  expect(snapshots).toContain("/api/v1/views/home_dashboard/index");

  await page.getByRole("link", { name: "View all tasks" }).click();
  await expect(page).toHaveURL(/\/tasks$/);
  await expect(page.getByRole("table", { name: "Tasks" })).toBeVisible();
});

test("Home uses canonical Worker and Artifact navigation", async ({ page }) => {
  await page.goto("/");

  const workerSnapshotResponse = page.waitForResponse((response) => (
    new URL(response.url()).pathname === "/api/v1/views/worker_detail/worker-home-idle"
  ));
  await page.getByRole("link", { name: /Confirm the Home dashboard contract.*worker-home-idle/ }).click();
  const workerSnapshot = await (await workerSnapshotResponse).json();
  await expect(page).toHaveURL(/\/workers\/worker-home-idle$/);
  await expect(page.getByLabel("Execution status")).toContainText("TaskWaiting User");
  await expect(page.getByText("Generation 2", { exact: true })).toBeVisible();
  await expect(page.getByText("task-switchback-counter", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Accept" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Request changes" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Send" })).toBeDisabled();

  await page.goto("/");
  const artifactSnapshotResponse = page.waitForResponse((response) => (
    new URL(response.url()).pathname === "/api/v1/views/artifact_detail/artifact-home-report"
  ));
  await page.getByRole("link", { name: "report artifact-home-report" }).click();
  const artifactSnapshot = await (await artifactSnapshotResponse).json();
  await expect(page).toHaveURL(/\/artifacts\/artifact-home-report$/);
  await expect(page.locator(".artifact-heading").getByRole("heading", { name: "Home delivery report" })).toBeVisible();
  const provenance = page.getByLabel("Artifact provenance");
  await expect(provenance.getByText("run-home-delivery", { exact: true })).toBeVisible();
  await expect(provenance.getByText("turn-home-delivery", { exact: true })).toBeVisible();
  await expect(provenance).toContainText("Generation2");
  await expect(provenance.getByText(`sha256:${"b".repeat(64)}`, { exact: true })).toBeVisible();
  await expect(page.getByText("Checksum verified", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /Return to Worker/ })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Download artifact" }))
    .toHaveAttribute("href", "/api/v1/artifacts/artifact-home-report/content");

  const workerArtifact = workerSnapshot.data.artifacts[0];
  expect(artifactSnapshot.data).toMatchObject(workerArtifact);
  expect(artifactSnapshot.data).toMatchObject({
    artifact_id: "artifact-home-report",
    worker_run_id: "run-home-delivery",
    agent_turn_id: "turn-home-delivery",
    runtime_generation: 2,
    checksum: `sha256:${"b".repeat(64)}`,
    content: "## Home delivery report\n\nThe Home projection links to this verified delivery report.",
    download_url: "/api/v1/artifacts/artifact-home-report/content",
    review_manifest: {
      schema_version: "artifact.review_manifest.v1",
      outcome: "online",
      renderable: true,
      download_allowed: true,
      media_type: "text/markdown",
      content_size_bytes: 84,
      diagnostics: [],
    },
  });
  expect(artifactSnapshot.available_actions).toEqual([{
    action: "artifact.download",
    target_id: "artifact-home-report",
    expected_revision: 1,
  }]);
  expect(artifactSnapshot.related_refs).toEqual([]);
});

test("Frontier fixture rejects unknown Worker and Artifact identities", async ({ page }) => {
  const responses: Array<{ path: string; status: number }> = [];
  page.on("response", (response) => {
    const path = new URL(response.url()).pathname;
    if (path.startsWith("/api/v1/views/")) {
      responses.push({ path, status: response.status() });
    }
  });

  await page.goto("/workers/worker-unknown");
  await expect(page.getByRole("alert")).toContainText("Unable to load Worker detail");
  expect(responses).toContainEqual({
    path: "/api/v1/views/worker_detail/worker-unknown",
    status: 404,
  });

  await page.goto("/artifacts/artifact-unknown");
  await expect(page.getByRole("alert")).toContainText("Unable to load Artifact");
  expect(responses).toContainEqual({
    path: "/api/v1/views/artifact_detail/artifact-unknown",
    status: 404,
  });
});

test("Home navigation does not create an Assistant conversation before explicit confirmation", async ({ page }) => {
  const commands: Array<Record<string, unknown>> = [];
  await page.route("**/api/v1/assistant/conversations", async (route) => {
    commands.push(route.request().postDataJSON());
    await route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({
        command_id: "create-browser",
        conversation_id: "conversation-browser",
        conversation_revision: 8,
        projection_ref: "/api/v1/views/assistant_conversation/conversation-browser",
        status: "accepted",
      }),
    });
  });

  await page.goto("/");
  await page.getByRole("link", { name: "Start Assistant conversation" }).click();
  await expect(page).toHaveURL(/\/assistant$/);
  await expect(page.getByRole("button", { name: "开始 Assistant 对话" })).toBeVisible();
  expect(commands).toHaveLength(0);

  await page.getByRole("button", { name: "开始 Assistant 对话" }).click();
  await expect(page).toHaveURL(/\/assistant\/conversation-browser$/);
  expect(commands).toHaveLength(1);
  expect(commands[0]).toMatchObject({
    schema_version: "assistant.conversation_command.v1",
    command_type: "create_conversation",
  });
});
