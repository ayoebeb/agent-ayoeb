import { createFileRoute } from "@tanstack/react-router";
import { MachinesView } from "../components/MachinesView";

export const Route = createFileRoute("/_shell/machines")({ component: MachinesView });
