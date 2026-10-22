import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { Status } from "../api/types";
import { StatusBadge, StatusDot, statusBg } from "./StatusBadge";

describe("StatusBadge", () => {
  afterEach(cleanup);

  it("labels degraded on its own, in amber like pending", () => {
    render(<StatusBadge status="degraded" />);
    const badge = screen.getByText("Degraded");
    expect(badge).toHaveClass("text-pend-ink");
    expect(screen.queryByText("Pending")).not.toBeInTheDocument();
    expect(statusBg("degraded")).toBe("bg-pend tone-dots");
  });

  it("names every status", () => {
    const want: Record<Status, string> = {
      up: "Up",
      down: "Down",
      pending: "Pending",
      maintenance: "Maintenance",
      degraded: "Degraded",
    };
    for (const [status, label] of Object.entries(want) as [Status, string][]) {
      const { unmount } = render(<StatusDot status={status} />);
      expect(screen.getByLabelText(label)).toBeInTheDocument();
      unmount();
    }
  });

  it("shows no data without a status", () => {
    render(<StatusBadge status={null} />);
    expect(screen.getByText("No data")).toBeInTheDocument();
  });
});
