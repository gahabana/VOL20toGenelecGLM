# VOL20toGenelecGLM
Python script that runs on the same machine where Genelec GLM is running and takes input from Fosi VOL20 and converts it to MIDI messages for GLM
Steps to get it up and running on Windows (10 and 11 tested)
1. Genelec GLM (v5.11 tested) installed
2. Python (3.13.1 tested) installed for Windows from www.python.org
3. LoopMIDI (i dont have physical MIDI HW so needed SW/Virtual one). Install and configure MIDI port called 'GLMMIDI'. Name is hardcoded in a script but if you change it to something else, it needs to be changed in the code as well. Channel name is same as the one given with added 'space' and '1' ... assuming you selected MIDI channel #1 in GLM
4. Genelec GLM needs to be configured for MIDI. Click on Settings/Midi and activate 'Enable MIDI'. I've also increased Volume Up and down to be 1dB instead of default 0.5dB
<img width="916" alt="image" src="https://github.com/user-attachments/assets/5255c96f-3469-4b64-a6a5-feaea0f4ff09" />

5. Python libraries need to be installed. 'hidapi' is usually easy. 'mido' has prereq 'python-rtmidi' which does require recompilation so need to install Windows SDK/C++ compiler from Microsoft. Google on how to do it
6. few other standard libraries are needed too but they are easy to be installed. Python will complaign on what is missing until i improve instructions
7. Bluetooth connection: Fosi VOL20 needs to be paired with Windows machine. In my testing the VID/PID of 'VOL20' device on windows was always as below, but please check on your machine: (VID and PID are hardcoded in the script):
  VID = 0x07d7
  PID = 0x0000
8. That's it for now 
