import body from "./assets/fold-engineer/body.webp";
import eyes from "./assets/fold-engineer/eyes.webp";
import mouth from "./assets/fold-engineer/mouth.webp";
import wrench from "./assets/fold-engineer/wrench.webp";
import arm from "./assets/fold-engineer/arm.webp";
import feet from "./assets/fold-engineer/feet.webp";
import "./fold-avatar-engineer.css";

const parts = { body, eyes, mouth, wrench, arm, feet };

function Part({
  part,
  x,
  y,
  width,
  height,
}: {
  part: keyof typeof parts;
  x: number;
  y: number;
  width: number;
  height: number;
}) {
  return <image href={parts[part]} x={x} y={y} width={width} height={height} />;
}

/** Approved Eli artwork, assembled as a transparent clay puppet. */
export function FoldAvatarEngineer() {
  return (
    <svg
      className="fold-avatar__svg fold-engineer"
      viewBox="0 0 240 240"
      fill="none"
      aria-hidden="true"
      focusable="false"
    >
      <ellipse cx="128" cy="212" rx="69" ry="5" fill="#171923" opacity=".12" />
      <Part part="feet" x={81} y={165} width={120} height={50} />
      <g className="fold-engineer__body">
        <Part part="arm" x={191} y={116} width={34} height={44} />
        {/* The shoulder sits behind the torso; the hand and tool remain outside it. */}
        <g className="fold-engineer__wrench">
          <Part part="wrench" x={13} y={47} width={92} height={137} />
        </g>
        <Part part="body" x={60} y={34} width={150} height={144} />
        <g className="fold-engineer__eyes">
          <Part part="eyes" x={95} y={80} width={80} height={45} />
        </g>
        <Part part="mouth" x={124} y={124} width={30} height={15} />
      </g>
    </svg>
  );
}
