import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { TaskIndexData } from "../../api/contracts";
import { TasksPage } from "./TasksPage";

const taskCommandMocks = vi.hoisted(() => ({
  createTask: vi.fn(),
  newTaskIdempotencyKey: vi.fn(() => "web-task-fixed"),
}));

vi.mock("../../api/taskCommands", () => taskCommandMocks);

const data: TaskIndexData = {
  tasks: [
    {
      task_id: "task-running",
      title: "Running delivery",
      workspace: "workspace-a",
      status: "running",
      status_tone: "active",
      worker_session_id: "session-exact",
      updated_at: "2026-07-29T00:00:00Z",
    },
    {
      task_id: "task-completed",
      title: "Completed delivery",
      workspace: "workspace-a",
      status: "completed",
      status_tone: "success",
      updated_at: "2026-07-28T00:00:00Z",
    },
    {
      task_id: "task-failed",
      title: "Failed delivery",
      workspace: "workspace-b",
      status: "failed",
      status_tone: "danger",
      updated_at: "2026-07-27T00:00:00Z",
    },
  ],
  capacity: { used: 12, limit: 10 },
};

afterEach(() => {
  taskCommandMocks.createTask.mockReset();
  taskCommandMocks.newTaskIdempotencyKey.mockClear();
});

describe("TasksPage", () => {
  it("filters active, completed, and all Tasks while preserving server status", () => {
    render(<TasksPage data={data} onNavigate={vi.fn()} />);

    expect(screen.getByText("Running delivery")).toBeInTheDocument();
    expect(screen.queryByText("Completed delivery")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Completed" }));
    expect(screen.getByText("Completed delivery")).toBeInTheDocument();
    expect(screen.getByText("Failed delivery")).toBeInTheDocument();
    expect(screen.queryByText("Running delivery")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "All" }));
    expect(screen.getByText("running")).toBeInTheDocument();
    expect(screen.getByText("completed")).toBeInTheDocument();
    expect(screen.getByText("failed")).toBeInTheDocument();
  });

  it("submits the existing direct TaskSpec command and reports accepted projection lag", async () => {
    taskCommandMocks.createTask.mockResolvedValue({ command_id: "command-1", status: "accepted" });
    render(<TasksPage data={data} onNavigate={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", { name: "Create task" }));
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "  Direct task  " } });
    fireEvent.change(screen.getByLabelText("Goal"), { target: { value: "  Preserve commands  " } });
    fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "kimi-codex-anthropic-v1" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(taskCommandMocks.createTask).toHaveBeenCalledWith(fetch, {
      idempotencyKey: "web-task-fixed",
      title: "Direct task",
      goal: "Preserve commands",
      agentProfileId: "kimi-codex-anthropic-v1",
      acceptanceMode: "manual",
    }));
    expect(screen.getByRole("status")).toHaveTextContent("Task accepted. The live index will update when projection catches up.");
  });

  it("routes Assistant through the explicit start page and only exact Workers through Worker links", () => {
    const onNavigate = vi.fn();
    render(<TasksPage data={data} onNavigate={onNavigate} />);

    fireEvent.click(screen.getByRole("button", { name: "Start Assistant conversation" }));
    expect(onNavigate).toHaveBeenLastCalledWith("/assistant");

    fireEvent.click(screen.getByRole("button", {
      name: "Open Worker session-exact for Running delivery",
    }));
    expect(onNavigate).toHaveBeenLastCalledWith("/workers/session-exact");
    fireEvent.click(screen.getByRole("button", { name: "All" }));
    const completedRow = screen.getByRole("row", { name: /Completed delivery/ });
    expect(within(completedRow).queryByRole("button", { name: /Open Worker/ })).not.toBeInTheDocument();
  });

  it("uses native table headers and keeps the exact Worker action inside its cell", () => {
    render(<TasksPage data={data} onNavigate={vi.fn()} />);

    const table = screen.getByRole("table", { name: "Tasks" });
    expect(table.tagName).toBe("TABLE");
    expect(within(table).getAllByRole("columnheader").map((header) => header.textContent))
      .toEqual(["Task", "Status", "Workspace", "Updated", "Worker"]);
    const row = within(table).getByRole("row", { name: /Running delivery/ });
    expect(row.tagName).toBe("TR");
    expect(within(row).getAllByRole("cell")).toHaveLength(5);
    expect(within(row).getByRole("button", {
      name: "Open Worker session-exact for Running delivery",
    })).toBeVisible();
    expect(table.parentElement).toHaveClass("task-table-scroll");
  });

  it("shows authoritative over-capacity values without masking the excess", () => {
    render(<TasksPage data={data} onNavigate={vi.fn()} />);

    expect(screen.getByText("12")).toBeInTheDocument();
    expect(screen.getByText("of 10 sessions")).toBeInTheDocument();
    expect(screen.getByText("120% · 2 over capacity")).toBeInTheDocument();
  });

  it("suspends direct Task creation while projection authority is unavailable", () => {
    const { rerender } = render(
      <TasksPage data={data} mutationsEnabled onNavigate={vi.fn()} />,
    );

    const createTask = screen.getByRole("button", { name: "Create task" });
    fireEvent.click(createTask);
    expect(screen.getByLabelText("Title")).toBeEnabled();

    rerender(<TasksPage data={data} mutationsEnabled={false} onNavigate={vi.fn()} />);
    expect(createTask).toBeDisabled();
    expect(screen.getByLabelText("Title")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Create" })).toBeDisabled();
    expect(screen.getByText("Task creation is suspended until the projection is live.")).toBeVisible();
  });

  it("revalidates current projection authority at the direct Task command boundary", () => {
    const isMutationAuthorized = vi.fn().mockReturnValue(false);
    render(
      <TasksPage
        data={data}
        isMutationAuthorized={isMutationAuthorized}
        mutationsEnabled
        onNavigate={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Create task" }));
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Do not submit" } });
    fireEvent.change(screen.getByLabelText("Goal"), { target: { value: "Authority changed" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    expect(isMutationAuthorized).toHaveBeenCalledTimes(1);
    expect(taskCommandMocks.createTask).not.toHaveBeenCalled();
    expect(screen.getByRole("status")).toHaveTextContent(
      "Task creation is suspended until the projection is live.",
    );
  });
});
