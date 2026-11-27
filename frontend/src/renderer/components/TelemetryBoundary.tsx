import React, { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { bindRendererErrorHandlers, captureRendererException } from "../lib/telemetry";
import { Button } from "./ui/button";

// The owner's wording for the fallback. There is no Dutch locale, and the screen
// of last resort should not depend on translations having loaded.
const FALLBACK_TITLE = "Er ging iets mis";
const RELOAD_LABEL = "Reload";

type Props = {
	children: React.ReactNode;
};

type BoundaryProps = Props & {
	fallbackBody: string;
};

type State = {
	error: string | null;
};

class TelemetryErrorBoundary extends React.Component<BoundaryProps, State> {
	state: State = { error: null };

	static getDerivedStateFromError(error: unknown): State {
		return { error: error instanceof Error ? `${error.name}: ${error.message}` : String(error) };
	}

	componentDidCatch(error: Error, info: React.ErrorInfo) {
		// Main writes renderer console errors to the window diagnostics log.
		console.error(`AO: app error screen shown: ${error?.stack ?? String(error)}${info.componentStack ?? ""}`);
		void captureRendererException(error, {
			source: "react-error-boundary",
			operation: "react_render",
		});
	}

	render() {
		if (this.state.error !== null) {
			return (
				<div role="alert" className="flex h-screen items-center justify-center bg-background px-6 text-center text-foreground">
					<div className="flex max-w-xl flex-col items-center gap-3">
						<h1 className="text-heading-sm font-semibold">{FALLBACK_TITLE}</h1>
						<p className="text-sm text-muted-foreground">{this.props.fallbackBody}</p>
						<pre className="max-h-48 w-full select-text overflow-auto whitespace-pre-wrap break-words text-left text-caption text-muted-foreground">
							{this.state.error}
						</pre>
						<Button onClick={() => window.location.reload()}>{RELOAD_LABEL}</Button>
					</div>
				</div>
			);
		}
		return this.props.children;
	}
}

export function TelemetryBoundary({ children }: Props) {
	const { t } = useTranslation();
	useEffect(() => { bindRendererErrorHandlers(); }, []);
	return <TelemetryErrorBoundary fallbackBody={t("appError.body")}>{children}</TelemetryErrorBoundary>;
}
