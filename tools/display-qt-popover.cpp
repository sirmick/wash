// Real-Qt test guest for the menu-fallback popover path (DISPLAY.md §12).
//
// Qt only creates a grabbing xdg_popup for a menu when it has a fresh input
// serial. Opened programmatically (no serial) Qt instead maps a parented,
// untitled xdg_toplevel — which wash-display must stream as a popup overlay
// on the parent's window, NOT as a standalone wash window. A QDialog (a real,
// titled toplevel) is the control: it must stay a window.
//
// Built on the fly by e2e/tests/display-qt-popover.spec.ts when Qt6 Widgets
// dev is present (skipped otherwise — no compositor/Qt in CI). No install.
//
//   build: g++ -std=c++17 display-qt-popover.cpp -o <out> \
//              $(pkg-config --cflags --libs Qt6Widgets) -fPIC
#include <QApplication>
#include <QWidget>
#include <QPushButton>
#include <QMenu>
#include <QDialog>
#include <QLabel>
#include <QTimer>

int main(int argc, char** argv) {
    QApplication app(argc, argv);
    QWidget w;
    w.setWindowTitle("qtpopover");
    w.resize(400, 300);
    auto* btn = new QPushButton("anchor", &w);
    btn->setGeometry(20, 20, 360, 40);
    w.show();

    // Programmatic menu (no input serial) → Qt maps a parented toplevel.
    QTimer::singleShot(3000, [&w]() {
        QMenu* m = new QMenu(&w);
        m->addAction("Alpha");
        m->addAction("Beta");
        m->addAction("Gamma");
        m->popup(w.mapToGlobal(QPoint(30, 80)));
    });
    // Control: a real titled dialog must remain a wash window.
    QTimer::singleShot(6000, [&w]() {
        QDialog* d = new QDialog(&w);
        d->setWindowTitle("Settings Dialog");
        d->resize(300, 200);
        (void)new QLabel("a real dialog", d);
        d->show();
    });
    return app.exec();
}
