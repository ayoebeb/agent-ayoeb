import { useEffect, useRef, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent, type RefObject } from "react";
import { useTranslation } from "react-i18next";
import { MIN_PANE_PX, ratioVar, type CssDivider } from "../lib/pane-split";

const KEYBOARD_STEP = 0.05;

/**
 * The boundary between two panes of a split. Dragging rewrites only that
 * split's ratio variable on the screen element, so no pane re-renders until
 * the drag ends; terminals refit through their own resize observers.
 */
export function SplitDivider({
	screenRef,
	divider,
	onResize,
}: {
	screenRef: RefObject<HTMLElement | null>;
	divider: CssDivider;
	onResize: (path: string, ratio: number) => void;
}) {
	const { t } = useTranslation();
	const row = divider.dir === "row";
	const dragRef = useRef<{ frame: number | null; ratio: number } | null>(null);
	useEffect(() => () => {
		const drag = dragRef.current;
		if (!drag) return;
		dragRef.current = null;
		if (drag.frame !== null) cancelAnimationFrame(drag.frame);
		document.body.classList.remove("is-resizing-x");
		document.body.style.cursor = "";
	}, []);
	const ratioNow = () => {
		const value = Number.parseFloat(screenRef.current?.style.getPropertyValue(ratioVar(divider.path)) ?? "");
		return Number.isFinite(value) ? value : 0.5;
	};
	// The split's own box never depends on its own ratio, so it can be measured
	// once per move from the screen and the layout's fractions.
	const clampRatio = (ratio: number) => {
		const rect = screenRef.current?.getBoundingClientRect();
		const span = rect ? (row ? divider.box.w * rect.width : divider.box.h * rect.height) : 0;
		const min = span > 0 ? Math.min(0.45, MIN_PANE_PX / span) : 0.1;
		return Math.min(1 - min, Math.max(min, ratio));
	};
	const ratioAt = (x: number, y: number) => {
		const rect = screenRef.current?.getBoundingClientRect();
		if (!rect || rect.width <= 0 || rect.height <= 0) return ratioNow();
		const ratio = row
			? (x - rect.left - divider.box.x * rect.width) / (divider.box.w * rect.width)
			: (y - rect.top - divider.box.y * rect.height) / (divider.box.h * rect.height);
		return clampRatio(ratio);
	};
	const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
		if (event.button !== 0) return;
		event.preventDefault();
		event.currentTarget.setPointerCapture(event.pointerId);
		dragRef.current = { frame: null, ratio: ratioNow() };
		// Shared with the shell resize grips: terminals do their final fit when
		// the class goes away.
		document.body.classList.add("is-resizing-x");
		if (!row) document.body.style.cursor = "row-resize";
	};
	const onPointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
		const drag = dragRef.current;
		if (!drag) return;
		drag.ratio = ratioAt(event.clientX, event.clientY);
		if (drag.frame !== null) return;
		drag.frame = requestAnimationFrame(() => {
			drag.frame = null;
			screenRef.current?.style.setProperty(ratioVar(divider.path), String(drag.ratio));
		});
	};
	const endDrag = () => {
		const drag = dragRef.current;
		if (!drag) return;
		dragRef.current = null;
		if (drag.frame !== null) cancelAnimationFrame(drag.frame);
		screenRef.current?.style.setProperty(ratioVar(divider.path), String(drag.ratio));
		document.body.classList.remove("is-resizing-x");
		document.body.style.cursor = "";
		onResize(divider.path, drag.ratio);
	};
	const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
		const keys = row ? { ArrowLeft: -1, ArrowRight: 1 } : { ArrowUp: -1, ArrowDown: 1 };
		const direction = keys[event.key as keyof typeof keys];
		if (direction === undefined) return;
		event.preventDefault();
		onResize(divider.path, clampRatio(ratioNow() + direction * KEYBOARD_STEP));
	};
	return (
		<div
			aria-label={t("splitPane.resize")}
			aria-orientation={row ? "vertical" : "horizontal"}
			aria-valuemax={100}
			aria-valuemin={0}
			aria-valuenow={Math.round(divider.ratio * 100)}
			className="pane-split-divider"
			data-orientation={row ? "vertical" : "horizontal"}
			data-testid="split-divider"
			onDoubleClick={() => onResize(divider.path, 0.5)}
			onKeyDown={onKeyDown}
			onLostPointerCapture={endDrag}
			onPointerCancel={endDrag}
			onPointerDown={onPointerDown}
			onPointerMove={onPointerMove}
			onPointerUp={endDrag}
			role="separator"
			style={row
				? { left: divider.style.left, top: divider.style.top, height: divider.style.height }
				: { left: divider.style.left, top: divider.style.top, width: divider.style.width }}
			tabIndex={0}
		/>
	);
}
