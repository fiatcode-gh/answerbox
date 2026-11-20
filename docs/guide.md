# Teacher guide

How Answerbox behaves, page by page. For the file format see [exam-format.md](exam-format.md).

## Accounts

- Teachers sign in with Google. Only verified emails on the teacher list can sign in.
- Each teacher sees only their own exams and results. Admins (the emails in `ANSWERBOX_ADMIN_EMAILS`) see every exam and manage the teacher list on the Teachers page.
- Sessions last 12 hours and survive restarts. There is no password: recovery is a settings change and a restart.

## Exam codes

- Every exam gets a 6-character code of capital letters and digits without look-alikes (no I, L, O, U, 0 or 1). It never changes, and no other exam ever gets it. A copy gets a new code.
- The exam page shows the code as large tiles, the student link `<ANSWERBOX_PUBLIC_URL>/?code=XXXXXX` with **Copy link**, and **Projector view** for the room.
- Students type the code and their name, or open the link and type only their name. Any case works; spaces and dashes are ignored.
- A code works only while its exam is open. A wrong code, a closed exam and a passed close time all read "No open exam has that code. Check it with your teacher."
- Several exams can be open at once. Closing an exam stops new starts only; attempts in progress continue.
- A student with an exam in progress is sent back to it. If they lose the browser, the **Resume** tab with their resume code (shown in the results) brings them back, also on another device.

## Schedule

- **Set a schedule** on the exam page takes an open time, a close time, or both, in `ANSWERBOX_TIME_ZONE`.
- Without an open time the exam opens when you save. Without a close time it stays open until you press **Close**.
- The close time stops new starts only; attempts in progress keep their full length.
- A student who comes early sees the exam title and "This exam opens at 08:00."
- **Open** and **Close** remove the schedule.

## Writing exams

- **New exam** writes an exam in the browser; **Import from file** takes a text file or a zip with images.
- **Edit questions** lists the questions with Edit, Move up, Move down and Remove, plus **Add question** and **Settings** (title, shuffle, result shown, monitor, length).
- A question is multiple choice (2 to 10 options, one correct) or fill-in (1 to 10 accepted answers). Switching between the two keeps both lists until you save.
- A question can add an image (PNG, JPEG, GIF or WebP, up to 5 MB). Saving adds a line such as `![](3f2a….png)` to the question text; move that line to place the image elsewhere.
- If someone else saved the same exam after you opened the page, your save is refused and the typed text stays in the form.
- **Once a student has started, the exam is frozen.** To change it, copy it.

## Sections

- In the editor, **Start a section here** begins one at a question; each section has **Edit section** and **Remove section**. Removing a section joins its questions to the section before (section 1 joins the next one).
- With shuffling on, questions shuffle inside each section, and sections keep their order.
- Results and the CSV get one score column per section.

## Fill-in questions

- The student types a short answer (up to 200 characters). It saves on its own, like a picked option.
- Matching ignores case and extra spaces, and reads Indonesian numbers by value; see [exam-format.md](exam-format.md#how-fill-in-answers-are-matched).
- After an attempt ends, **Mark right** or **Mark wrong** on the attempt page overrides the automatic mark, and **Remove override** returns to it. The score changes everywhere, and the student is not told.

## Results

- **Results** lists every attempt: status, score, a column per section, time left and resume code while in progress, and the monitor's flags. **View** opens every answer of an attempt.
- Sort by Name or Score, filter by name, and **Export CSV** for a spreadsheet.
- While an attempt is in progress or the exam is open, the list refreshes itself every 10 seconds and keeps your filter and sort.
- Beside Results, the exam page counts attempts in progress, submitted and timed out.

## Revisions, download and copy

- Every import, Replace and editor save makes a numbered revision. **History** lists them, and each opens read-only.
- Each attempt records the revision it was taken on.
- **Download** gives the current revision as an exam file (a zip when it has images). Importing it gives the same exam.
- **Copy exam** makes a new, closed exam with a new code, owned by you. Attempts and the schedule are not copied.

## Archive

- **Archive** hides an exam from the list and closes it. It asks first; **Unarchive** is the undo and returns the exam closed.
- Results, the CSV, History, Download and Copy keep working on an archived exam.
- **Show archived** lists every exam, with archived ones marked.

## Removing things

Remove question, Remove section, Remove teacher, Archive and Delete attempt each open a confirmation page first. They work the same with scripts turned off.

## Look

Pages follow the device's light or dark setting. Student pages and question text use Atkinson Hyperlegible Next, served by the app itself. Every teacher page and the start page work without scripts; scripts only add Copy link, the refreshing results and the code boxes' typing help.
