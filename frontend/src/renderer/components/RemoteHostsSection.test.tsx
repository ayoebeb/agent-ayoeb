import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { RemoteHostsSection } from "./RemoteHostsSection";
import { SidebarMenu, SidebarMenuItem, SidebarProvider } from "./ui/sidebar";

it("keeps host-specific offline controls around shared project rows", () => {
	const onRetry = vi.fn();
	const renderProject = vi.fn((host: { hostId: string }, workspace: { name: string }) =>
		<SidebarMenuItem data-testid={`project-${host.hostId}`}>{workspace.name}</SidebarMenuItem>);
	render(<SidebarProvider><SidebarMenu><RemoteHostsSection
		hosts={[
			{ hostId: "box-a", label: "Box A", url: "https://a.test", status: "connected" },
			{ hostId: "box-b", label: "Box B", url: "https://b.test", status: "offline", failureReason: "unauthorized" },
		]}
		workspaces={[
			{ hostId: "box-a", id: "shared", name: "Project A", path: "/a", sessions: [] },
			{ hostId: "box-b", id: "shared", name: "Project B", path: "/b", sessions: [] },
		]}
		renderProject={renderProject}
		onRetry={onRetry}
	/></SidebarMenu></SidebarProvider>);
	expect(screen.getByTestId("project-box-a")).toHaveTextContent("Project A");
	expect(screen.queryByTestId("project-box-b")).toBeNull();
	expect(screen.queryByRole("button", { name: "New project on Box A" })).toBeNull();
	expect(screen.getByText("Password rejected")).toBeVisible();
	fireEvent.click(screen.getByRole("button", { name: "Retry Box B" }));
	expect(onRetry).toHaveBeenCalledOnce();
});

it("offers the Computers page only when it can open one and a host is linked", () => {
	const onOpenComputers = vi.fn();
	const props = { workspaces: [], renderProject: vi.fn(), onRetry: vi.fn() };
	const host = { hostId: "box-a", label: "Box A", url: "https://a.test", status: "connected" as const };
	const { rerender } = render(<SidebarProvider><SidebarMenu><RemoteHostsSection {...props} hosts={[host]} onOpenComputers={onOpenComputers} /></SidebarMenu></SidebarProvider>);
	fireEvent.click(screen.getByRole("button", { name: "Computers" }));
	expect(onOpenComputers).toHaveBeenCalledOnce();

	rerender(<SidebarProvider><SidebarMenu><RemoteHostsSection {...props} hosts={[host]} /></SidebarMenu></SidebarProvider>);
	expect(screen.queryByRole("button", { name: "Computers" })).toBeNull();
	rerender(<SidebarProvider><SidebarMenu><RemoteHostsSection {...props} hosts={[]} onOpenComputers={onOpenComputers} /></SidebarMenu></SidebarProvider>);
	expect(screen.queryByRole("button", { name: "Computers" })).toBeNull();
});

it("shows the last saved projects of a host that is offline or connecting", () => {
	const renderProject = vi.fn((host: { hostId: string }, workspace: { name: string }) =>
		<SidebarMenuItem data-testid={`project-${host.hostId}`}>{workspace.name}</SidebarMenuItem>);
	render(<SidebarProvider><SidebarMenu><RemoteHostsSection
		hosts={[
			{ hostId: "box-a", label: "Box A", url: "https://a.test", status: "offline" },
			{ hostId: "box-b", label: "Box B", url: "https://b.test", status: "connecting" },
		]}
		workspaces={[
			{ hostId: "box-a", id: "shared", name: "Project A", path: "/a", sessions: [] },
			{ hostId: "box-b", id: "shared", name: "Project B", path: "/b", sessions: [] },
		]}
		renderProject={renderProject}
		onRetry={vi.fn()}
	/></SidebarMenu></SidebarProvider>);
	expect(screen.getByRole("button", { name: "Retry Box A" })).toBeVisible();
	expect(screen.getByTestId("project-box-a")).toHaveTextContent("Project A");
	expect(screen.getByTestId("project-box-b")).toHaveTextContent("Project B");
});
