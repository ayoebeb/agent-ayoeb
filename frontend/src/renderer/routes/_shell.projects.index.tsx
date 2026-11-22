import { createFileRoute } from "@tanstack/react-router";
import { ProjectsView } from "../components/ProjectsView";

export const Route = createFileRoute("/_shell/projects/")({ component: ProjectsView });
