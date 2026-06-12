// severityColor maps a syslog priority (0..7) to a line color. Shared
// by the log apps (journal, syslogs) so the severity palette lives in
// one place. Negative priority = unknown → neutral dim strip.
//
//   0..3  emerg/alert/crit/err  → red
//   4     warning               → amber
//   5     notice                → pale blue
//   6     info / unknown        → neutral
//   7     debug                 → dim

import { tokens } from './tokens';

export const severityColor = (priority: number): string => {
  if (priority < 0) return tokens.fgDim;
  if (priority <= 3) return tokens.sevError;
  if (priority === 4) return tokens.sevWarn;
  if (priority === 5) return tokens.sevNotice;
  if (priority === 7) return tokens.sevDebug;
  return tokens.sevInfo;
};
