import type { SVGProps } from 'react';

const base = { viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 1.8, strokeLinecap: 'round', strokeLinejoin: 'round' } as const;
type P = SVGProps<SVGSVGElement>;
const mk = (d: string) => (p: P) => (
  <svg {...base} {...p}>
    <path d={d} />
  </svg>
);

export const IconHome = mk('M3 11l9-7 9 7v9a1 1 0 0 1-1 1h-5v-6H9v6H4a1 1 0 0 1-1-1z');
export const IconKey = mk('M15 7a4 4 0 1 1-3.9 4.9L3 20v-3h3v-3h3l1.1-1.1A4 4 0 0 1 15 7zm1 2.5h.01');
export const IconShield = mk('M12 3l8 3v6c0 4.5-3.4 8.3-8 9-4.6-.7-8-4.5-8-9V6z');
export const IconCube = mk('M12 3l8 4.5v9L12 21l-8-4.5v-9zM12 12l8-4.5M12 12v9M12 12L4 7.5');
export const IconTicket = mk('M4 7h16v3a2 2 0 0 0 0 4v3H4v-3a2 2 0 0 0 0-4zM10 7v10');
export const IconChart = mk('M4 20V10M10 20V4M16 20v-7M22 20H2');
export const IconList = mk('M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01');
export const IconServer = mk('M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01');
export const IconGear = mk('M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z');
export const IconRefresh = mk('M21 12a9 9 0 1 1-2.6-6.4M21 4v5h-5');
export const IconPlus = mk('M12 5v14M5 12h14');
export const IconTrash = mk('M4 7h16M10 11v6M14 11v6M5 7l1 13h12l1-13M9 7V4h6v3');
export const IconCopy = mk('M9 9h11v11H9zM5 15H4V4h11v1');
export const IconLogout = mk('M15 4h4a1 1 0 0 1 1 1v14a1 1 0 0 1-1 1h-4M10 17l5-5-5-5M15 12H3');
export const IconMoon = mk('M21 13A9 9 0 1 1 11 3a7 7 0 0 0 10 10z');
export const IconSun = mk('M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10zM12 1v2M12 21v2M4.2 4.2l1.4 1.4M18.4 18.4l1.4 1.4M1 12h2M21 12h2M4.2 19.8l1.4-1.4M18.4 5.6l1.4-1.4');
export const IconMenu = mk('M3 6h18M3 12h18M3 18h18');
export const IconX = mk('M6 6l12 12M18 6L6 18');
export const IconDownload = mk('M12 3v12M7 10l5 5 5-5M4 21h16');
export const IconUpload = mk('M12 21V9M7 14l5-5 5 5M4 3h16');
export const IconExternal = mk('M14 4h6v6M20 4l-9 9M18 14v6H4V6h6');
export const IconEdit = mk('M4 20h4L19 9l-4-4L4 16zM14 6l4 4');
