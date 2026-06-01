// Component test (Tier B) for the bulk-ops sidebar widget — pure renderer of
// the bulk-ops job snapshot. Covers empty/populated, running-vs-terminal row
// shape (cancel button vs status text), progress fraction, and the cancel
// callback. The cross-process job lifecycle stays in bulkops.spec.ts.

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { BulkWidget, type BulkJob } from './BulkWidget.tsx';

afterEach(cleanup);

const job = (over: Partial<BulkJob> = {}): BulkJob => ({
  job_id: 'j1',
  op: 'delete',
  status: 'running',
  paths: ['/a', '/b'],
  dest: '',
  done: 0,
  total: 0,
  error: '',
  ...over,
});

test('empty: shows the empty hint, no job rows', () => {
  const { queryByTestId } = render(() => <BulkWidget jobs={() => []} onCancel={() => {}} />);
  expect(queryByTestId('bulk-empty')).toBeTruthy();
  expect(queryByTestId('bulk-job-j1')).toBeNull();
});

test('a running job shows a cancel button + progress fraction', () => {
  const { getByTestId, queryByTestId } = render(() => (
    <BulkWidget jobs={() => [job({ job_id: 'r', status: 'running', done: 2, total: 4 })]} onCancel={() => {}} />
  ));
  expect(getByTestId('bulk-cancel-r')).toBeTruthy();
  expect(queryByTestId('bulk-status-r')).toBeNull();
  expect(getByTestId('bulk-progress-r').getAttribute('data-fraction')).toBe('0.500');
});

test('a terminal job shows status text, no cancel button', () => {
  const { getByTestId, queryByTestId } = render(() => (
    <BulkWidget jobs={() => [job({ job_id: 'd', status: 'done', done: 4, total: 4 })]} onCancel={() => {}} />
  ));
  expect(queryByTestId('bulk-cancel-d')).toBeNull();
  expect(getByTestId('bulk-status-d').textContent).toContain('done');
});

test('clicking cancel fires onCancel with the job id', () => {
  const cancelled: string[] = [];
  const { getByTestId } = render(() => (
    <BulkWidget jobs={() => [job({ job_id: 'x', status: 'running' })]} onCancel={(id) => cancelled.push(id)} />
  ));
  fireEvent.click(getByTestId('bulk-cancel-x'));
  expect(cancelled).toEqual(['x']);
});
