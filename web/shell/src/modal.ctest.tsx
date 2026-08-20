// The session-modal layer (docs/SIDEBAR.md M4). What's asserted here is
// the security contract, not the styling: a modal that can appear on its
// own is a modal an attacker can imitate, so "never unbidden" and "one at
// a time" are the tests that matter.
//
// @vitest-environment jsdom

import { describe, it, expect, afterEach, beforeEach } from 'vitest';
import { render, cleanup, fireEvent } from '@solidjs/testing-library';
import { ModalLayer, registerModal, summonModal, dismissModal, hasModal, forgetModalsFor } from './modal.tsx';

const PRIV = 'com.wash.priv';

beforeEach(() => {
  forgetModalsFor('local');
  forgetModalsFor('build01');
  dismissModal();
});
afterEach(cleanup);

describe('the modal never opens itself', () => {
  it('a declared modal renders nothing until summoned', () => {
    registerModal({ origin: 'local', instanceID: 'i-1', element: 'wash-app-priv', appID: PRIV });
    const { queryByTestId } = render(() => <ModalLayer />);
    // Declared and booted with the session — but silent. This is the
    // property that makes an unbidden prompt provably a forgery.
    expect(queryByTestId('modal-layer')).toBeNull();
  });

  it('summoning an undeclared app does nothing rather than blurring over emptiness', () => {
    const { queryByTestId } = render(() => <ModalLayer />);
    expect(summonModal('build01', PRIV)).toBe(false);
    expect(queryByTestId('modal-layer')).toBeNull();
  });
});

describe('a summoned modal says whose it is', () => {
  it('names the host and the app, in chrome the app cannot draw', () => {
    registerModal({ origin: 'build01', instanceID: 'i-9', element: 'wash-app-priv', appID: PRIV });
    const { getByTestId } = render(() => <ModalLayer />);
    expect(summonModal('build01', PRIV)).toBe(true);
    const host = getByTestId('modal-host');
    // The app id comes from app.declared — the router's word — so this
    // line is the one an impersonator cannot reproduce.
    expect(host.textContent).toContain('build01');
    expect(host.textContent).toContain(PRIV);
    expect(getByTestId('modal-layer').getAttribute('data-origin')).toBe('build01');
  });

  it('a local modal says so rather than naming a host', () => {
    registerModal({ origin: 'local', instanceID: 'i-1', element: 'wash-app-priv', appID: PRIV });
    const { getByTestId } = render(() => <ModalLayer />);
    summonModal('local', PRIV);
    expect(getByTestId('modal-host').textContent).toContain('this machine');
  });
});

describe('one at a time', () => {
  it('a second summon replaces the first — two blurs would lie about attention', () => {
    registerModal({ origin: 'local', instanceID: 'i-1', element: 'wash-app-priv', appID: PRIV });
    registerModal({ origin: 'build01', instanceID: 'i-9', element: 'wash-app-priv', appID: PRIV });
    const { getAllByTestId, getByTestId } = render(() => <ModalLayer />);
    summonModal('local', PRIV);
    summonModal('build01', PRIV);
    expect(getAllByTestId('modal-layer')).toHaveLength(1);
    expect(getByTestId('modal-layer').getAttribute('data-origin')).toBe('build01');
  });
});

describe('declining to answer is always possible', () => {
  it('Escape dismisses', () => {
    registerModal({ origin: 'local', instanceID: 'i-1', element: 'wash-app-priv', appID: PRIV });
    const { queryByTestId } = render(() => <ModalLayer />);
    summonModal('local', PRIV);
    expect(queryByTestId('modal-layer')).toBeTruthy();
    fireEvent.keyDown(window, { key: 'Escape' });
    // The request stays queued either way — this only stops SHOWING it.
    expect(queryByTestId('modal-layer')).toBeNull();
  });

  it('clicking the backdrop dismisses, but clicking the frame does not', () => {
    registerModal({ origin: 'local', instanceID: 'i-1', element: 'wash-app-priv', appID: PRIV });
    const { queryByTestId, getByTestId } = render(() => <ModalLayer />);
    summonModal('local', PRIV);
    fireEvent.click(getByTestId('modal-frame'));
    expect(queryByTestId('modal-layer')).toBeTruthy();
    fireEvent.click(getByTestId('modal-layer'));
    expect(queryByTestId('modal-layer')).toBeNull();
  });
});

describe('a host that goes away takes its modal with it', () => {
  it('detaching drops the summoned modal rather than trapping the seat', () => {
    registerModal({ origin: 'build01', instanceID: 'i-9', element: 'wash-app-priv', appID: PRIV });
    const { queryByTestId } = render(() => <ModalLayer />);
    summonModal('build01', PRIV);
    expect(queryByTestId('modal-layer')).toBeTruthy();
    forgetModalsFor('build01');
    // A blur belonging to a dead host would be a question nobody can answer.
    expect(queryByTestId('modal-layer')).toBeNull();
    expect(hasModal('build01', PRIV)).toBe(false);
  });
});
