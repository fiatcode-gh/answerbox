# Exam file format

An exam is a plain text file. Write it in any editor, import it with **Import from file**, and download any exam back in the same format. [`sample-exam.md`](sample-exam.md) is a small complete exam to start from.

````markdown
# Chapter 3 quiz
shuffle: questions, options
show_result: review
duration: 30

## Part A: Multiple choice
Choose **one** answer.

1. What is the **area** of this triangle?
![A right triangle with legs 3 and 4](triangle.png)
- 6 *
- 7
- 12

2. What does this program print?
```
fmt.Println(2 + 3)
```
- `5` *
- `23`

## Part B: Short answers

3. What is the capital of Indonesia?
= Jakarta
= DKI Jakarta

4. How much is 1.000 + 3?
= 1.003
````

## The rules

- **Title:** the first line, `# Title`.
- **Settings** (optional, after the title):

  | Setting | Values | Default |
  | --- | --- | --- |
  | `shuffle:` | `none`, `questions`, `questions, options` | `questions` |
  | `show_result:` | `none`, `score`, `review` | `score` |
  | `monitor:` | `on`, `off` | `on` |
  | `duration:` | minutes, 5 to 240 | `60` |

- **Questions** are numbered `1.`, `2.`, … at the start of a line.
- **Multiple choice:** options start with `- `, at least two, and exactly one ends with ` *` (the correct one).
- **Fill-in:** instead of options, 1 to 10 accepted answers on lines starting with `= `, each up to 200 characters. A question has options or accepted answers, never both.
- **Sections:** a line `## Title` starts a section. The lines after it, up to the next question, are its instructions. The first section starts at question 1, and with shuffling on, questions shuffle inside their section.
- **Text** supports `**bold**`, `*italics*`, `` `code` ``, line breaks, fenced code blocks and images (`![description](file.png)`). Write `\-` or `\*` for a line that starts with `-` or an option that ends with `*`.
- **Images:** upload a `.zip` with the text file and the images (PNG, JPEG, GIF or WebP).
- **Limits:** 10 MB per upload, and the upload must finish within 60 seconds.

## How fill-in answers are matched

A typed answer is right when it matches one accepted answer, ignoring capital letters and extra spaces. Accents and punctuation count. Numbers written the Indonesian way match by value: `1.003` matches `1003`, `0,50` matches `0,5` and `007` matches `7`. `1.5` and `1.00` are not numbers that way, so they match only the same text. After an attempt ends, the teacher can still mark a typed answer right or wrong.

## The monitor

With `monitor: on`, the exam page blocks copy, cut, paste and the right-click menu. It tells the teacher when a student leaves the page for more than 3 seconds, tries a blocked action or resizes the window, and shows the student "Your teacher has been notified." The teacher sees the count in the results and the CSV, and each event on the attempt page.

The monitor cannot see other apps, other devices, screenshots or browser extensions. It discourages cheating; it does not prevent it. Events never change answers, time or score.
